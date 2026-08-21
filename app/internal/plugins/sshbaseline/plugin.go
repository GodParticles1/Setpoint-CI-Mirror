package sshbaseline

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"setpoint/internal/executor"
	"setpoint/internal/plugin"
	"setpoint/internal/plugins/checkutil"
	"setpoint/internal/task"
)

const ID = "ssh.baseline.core"

const defaultContextReason = "sshd -T was evaluated without -C connection attributes; included files are parsed for the default context, but Match outcomes for every possible connection are not proven"

var directiveDefinitions = []checkutil.Definition{
	{ID: "ssh.permit_empty_passwords", Name: "SSH 空密码登录", Recommended: "Setpoint 内置建议：no", Risk: "critical", Description: "允许空密码会导致账号被直接利用；最终目标仍应服从项目批准策略。", Remediation: "确认登录链路后将 PermitEmptyPasswords 设置为批准值。", MayAffectConnection: true},
	{ID: "ssh.permit_root_login", Name: "SSH root 直接登录", Recommended: "Setpoint 内置建议：no（可由任务参数覆盖）", Risk: "high", Description: "root 直接远程登录扩大高权限账号暴露面；并非所有项目都采用同一目标。", Remediation: "建立可用的普通管理账号和提权路径后再按批准策略调整。", MayAffectConnection: true},
	{ID: "ssh.max_auth_tries", Name: "SSH 最大认证尝试次数", Recommended: "Setpoint 内置建议：<= 4", Risk: "medium", Description: "过多认证尝试增加在线猜测机会；项目可根据登录链路批准其他目标。", Remediation: "确认运维登录方式后按批准策略调整 MaxAuthTries。", MayAffectConnection: true},
	{ID: "ssh.x11_forwarding", Name: "SSH X11 转发", Recommended: "Setpoint 内置建议：no", Risk: "medium", Description: "不需要的 X11 转发增加攻击面；图形运维场景可能有不同策略。", Remediation: "确认无图形转发需求后再禁用 X11Forwarding。", MayAffectConnection: true},
	{ID: "ssh.client_alive_interval", Name: "SSH 空闲探测间隔", Recommended: "Setpoint 内置建议：1-300 秒", Risk: "medium", Description: "未配置空闲探测会保留长期无人会话；目标值需结合现场网络质量。", Remediation: "结合现场网络质量设置 ClientAliveInterval。", MayAffectConnection: true},
	{ID: "ssh.client_alive_count_max", Name: "SSH 空闲探测次数", Recommended: "Setpoint 内置建议：0-3", Risk: "medium", Description: "过多探测次数会延长无人会话存活时间；目标值需结合探测间隔。", Remediation: "结合 ClientAliveInterval 设置批准值。", MayAffectConnection: true},
	{ID: "ssh.password_authentication", Name: "SSH 密码认证", Recommended: "Setpoint 内置建议：no（可由任务参数覆盖）", Risk: "high", Description: "密码认证更容易遭受口令猜测和凭据泄漏，但部分项目仍依赖受控密码登录。", Remediation: "仅在密钥登录和应急路径验证后按批准策略调整。", MayAffectConnection: true},
	{ID: "ssh.port", Name: "SSH 默认上下文端口观察", Recommended: "1-65535 且经现场批准", Risk: "low", Description: "端口是否符合要求取决于项目批准策略，端口变更属于高风险人工决策。", Remediation: "本插件不修改端口；端口迁移必须使用未来受控变更流程。", MayAffectConnection: true},
	{ID: "ssh.banner", Name: "SSH Banner 路径观察", Recommended: "配置经批准且内容有效的 Banner 路径", Risk: "low", Description: "sshd -T 只能确认默认上下文路径，不能证明 Banner 内容和所有 Match 场景。", Remediation: "确认警示文本、文件权限和适用连接上下文后配置 Banner。", MayAffectConnection: true},
	{ID: "ssh.ciphers", Name: "SSH 默认上下文加密算法", Recommended: "Setpoint 内置建议：不包含 3DES、RC4、CBC、arcfour", Risk: "high", Description: "弱算法可能降低会话机密性；无连接上下文的 sshd -T 不覆盖所有 Match 结果。", Remediation: "完成客户端兼容性评估后按批准策略收紧 Ciphers。", MayAffectConnection: true},
}

var syntaxDefinition = checkutil.Definition{
	ID: "ssh.syntax", Name: "sshd 配置语法", Recommended: "sshd -t 退出码 0", Risk: "high",
	Description: "语法错误可能导致 sshd 无法重载或重启。", Remediation: "修正配置后再次运行 sshd -t；本插件不重载服务。", MayAffectConnection: true,
}

var permissionDefinition = checkutil.Definition{
	ID: "ssh.config_permissions", Name: "sshd_config 权限和属主", Recommended: "root:root，0600 或更严格且不可被组/其他用户写入", Risk: "high",
	Description: "主配置属主异常或可被非特权用户写入会破坏 SSH 安全边界。", Remediation: "确认发行版约定后恢复 root:root 并收紧 /etc/ssh/sshd_config 权限。", MayAffectConnection: true,
}

type Plugin struct{}

func New() Plugin { return Plugin{} }

func (Plugin) Metadata() plugin.Metadata {
	all := append(append([]checkutil.Definition(nil), directiveDefinitions...), syntaxDefinition, permissionDefinition)
	all = append(all, listenerDefinitions...)
	checks := make([]plugin.CheckItemDefinition, 0, len(all))
	for _, definition := range all {
		checks = append(checks, plugin.CheckItemDefinition{
			ID: definition.ID, Name: definition.Name, Description: definition.Description,
			RecommendedValue: definition.Recommended, SourceRefs: sourceRefs(definition.ID),
		})
	}
	return plugin.Metadata{
		ID: ID, Category: "SSH", Name: "SSH 基础安全检查", Version: "2.2.0",
		Description: "通过 sshd 默认评估上下文、语法、主配置权限和可靠归属的监听端口只读评估 SSH；不声称覆盖所有 Match 连接上下文。",
		Mode:        plugin.ModeReadOnly, Risk: plugin.RiskLow, Impact: "只执行 sshd -T、sshd -t、ss -H -lntp 和固定配置文件权限读取",
		SupportedSystems: []string{"linux"},
		Parameters: []plugin.Parameter{
			{Name: "permit_root_login_target", Type: "string", Description: "项目批准的 PermitRootLogin 目标；默认 no", Options: []string{"no", "prohibit-password", "forced-commands-only", "yes"}},
			{Name: "password_authentication_target", Type: "string", Description: "项目批准的 PasswordAuthentication 目标；默认 no", Options: []string{"no", "yes"}},
		},
		Checks: checks,
	}
}

func (Plugin) Detect(ctx context.Context, input plugin.CheckInput) (plugin.Detection, error) {
	result, err := input.Executor.Execute(ctx, executor.Command{Name: "sshd", Args: []string{"-T"}})
	if errors.Is(err, executor.ErrCommandNotFound) {
		return plugin.Detection{Applicable: false, Reason: "sshd is not installed in trusted system directories"}, nil
	}
	if err != nil {
		return plugin.Detection{Applicable: true}, fmt.Errorf("read effective sshd configuration: %w", err)
	}
	if result.StdoutTruncated || result.StderrTruncated {
		return plugin.Detection{Applicable: true}, errors.New("sshd -T output exceeded the configured limit")
	}
	return plugin.Detection{Applicable: true}, nil
}

type policy struct {
	PermitRootLogin        string `json:"permit_root_login_target"`
	PasswordAuthentication string `json:"password_authentication_target"`
}

func parsePolicy(raw json.RawMessage) (policy, error) {
	result := policy{PermitRootLogin: "no", PasswordAuthentication: "no"}
	if len(raw) == 0 || string(raw) == "null" || string(raw) == "{}" {
		return result, nil
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return policy{}, fmt.Errorf("decode SSH check parameters: %w", err)
	}
	result.PermitRootLogin = strings.ToLower(strings.TrimSpace(result.PermitRootLogin))
	result.PasswordAuthentication = strings.ToLower(strings.TrimSpace(result.PasswordAuthentication))
	if !oneOf(result.PermitRootLogin, "no", "prohibit-password", "forced-commands-only", "yes") {
		return policy{}, fmt.Errorf("unsupported permit_root_login_target %q", result.PermitRootLogin)
	}
	if !oneOf(result.PasswordAuthentication, "no", "yes") {
		return policy{}, fmt.Errorf("unsupported password_authentication_target %q", result.PasswordAuthentication)
	}
	return result, nil
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func (Plugin) Check(ctx context.Context, input plugin.CheckInput) ([]task.CheckItem, error) {
	now := time.Now().UTC()
	selected := selectedCheckSet(input.SelectedCheckIDs)
	items := make([]task.CheckItem, 0, len(directiveDefinitions)+2+len(listenerDefinitions))
	var failures []error

	if anyDirectiveSelected(selected) {
		currentPolicy, policyErr := parsePolicy(input.Parameters)
		if policyErr != nil {
			for _, definition := range directiveDefinitions {
				if checkSelected(selected, definition.ID) {
					items = append(items, checkutil.Error(definition, "invalid_check_parameters", policyErr.Error(), "SSH check parameters were rejected before observation", now))
				}
			}
			return items, policyErr
		}

		effectiveResult, effectiveErr := input.Executor.Execute(ctx, executor.Command{Name: "sshd", Args: []string{"-T"}})
		if effectiveErr == nil && (effectiveResult.StdoutTruncated || effectiveResult.StderrTruncated) {
			effectiveErr = errors.New("sshd -T output exceeded the configured limit")
		}
		values := parseEffectiveConfig(effectiveResult.Stdout)
		for _, definition := range directiveDefinitions {
			if !checkSelected(selected, definition.ID) {
				continue
			}
			if effectiveErr != nil {
				items = append(items, checkutil.Error(definition, checkutil.ErrorCode(effectiveErr, "sshd_effective_config_failed"), effectiveErr.Error(), "Unable to obtain complete sshd -T output", now))
				continue
			}
			value, exists := values[directiveKey(definition.ID)]
			if !exists {
				missing := fmt.Errorf("sshd -T did not report %s", directiveKey(definition.ID))
				items = append(items, checkutil.Error(definition, "sshd_directive_missing", missing.Error(), "Effective directive is missing", now))
				failures = append(failures, missing)
				continue
			}
			items = append(items, directiveItem(definition, value, currentPolicy, now))
		}
		if effectiveErr != nil {
			failures = append(failures, effectiveErr)
		}
	}

	if checkSelected(selected, syntaxDefinition.ID) {
		syntaxResult, syntaxErr := input.Executor.Execute(ctx, executor.Command{Name: "sshd", Args: []string{"-t"}})
		if syntaxErr != nil {
			items = append(items, checkutil.Error(syntaxDefinition, checkutil.ErrorCode(syntaxErr, "sshd_syntax_failed"), syntaxErr.Error(), boundedSummary(syntaxResult.Stderr), now))
			failures = append(failures, syntaxErr)
		} else {
			items = append(items, checkutil.Value(syntaxDefinition, "valid", true, "sshd -t completed successfully without reloading the service", now))
		}
	}

	if checkSelected(selected, permissionDefinition.ID) {
		permissionResult, permissionErr := input.Executor.Execute(ctx,
			executor.Command{Name: "stat", Args: []string{"-c", "%a|%U|%G", "--", "/etc/ssh/sshd_config"}})
		if permissionErr != nil {
			items = append(items, checkutil.Error(permissionDefinition, checkutil.ErrorCode(permissionErr, "sshd_config_stat_failed"), permissionErr.Error(), "Unable to read sshd_config mode, owner and group", now))
			failures = append(failures, permissionErr)
		} else {
			items = append(items, sshConfigPermissionItem(permissionResult.Stdout, now))
		}
	}

	if anyListenerSelected(selected) {
		effectiveResult, effectiveErr := input.Executor.Execute(ctx, executor.Command{Name: "sshd", Args: []string{"-T"}})
		if effectiveErr == nil && (effectiveResult.StdoutTruncated || effectiveResult.StderrTruncated) {
			effectiveErr = errors.New("sshd -T output exceeded the configured limit")
		}
		if effectiveErr != nil {
			items = append(items, listenerErrors(selected, checkutil.ErrorCode(effectiveErr, "sshd_effective_config_failed"),
				effectiveErr, "Unable to obtain complete sshd -T output", now)...)
			failures = append(failures, effectiveErr)
		} else {
			listenerResult, listenerErr := input.Executor.Execute(ctx, executor.Command{
				Name: "ss", Args: []string{"-H", "-lntp"}, OutputLimit: executor.MaxOutputLimit,
			})
			if listenerErr == nil && (listenerResult.StdoutTruncated || listenerResult.StderrTruncated) {
				listenerErr = errors.New("ss -H -lntp output exceeded the configured limit")
			}
			if listenerErr != nil {
				items = append(items, listenerErrors(selected, checkutil.ErrorCode(listenerErr, "ssh_listener_read_failed"),
					listenerErr, "Unable to obtain complete TCP listener ownership output", now)...)
				failures = append(failures, listenerErr)
			} else {
				listenerResults, parseErr := listenerItems(effectiveResult.Stdout, listenerResult.Stdout, selected, now)
				items = append(items, listenerResults...)
				if parseErr != nil {
					failures = append(failures, parseErr)
				}
			}
		}
	}
	return items, errors.Join(failures...)
}

func selectedCheckSet(ids []string) map[string]struct{} {
	if len(ids) == 0 {
		return nil
	}
	selected := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		selected[id] = struct{}{}
	}
	return selected
}

func checkSelected(selected map[string]struct{}, id string) bool {
	if len(selected) == 0 {
		return true
	}
	_, exists := selected[id]
	return exists
}

func anyDirectiveSelected(selected map[string]struct{}) bool {
	for _, definition := range directiveDefinitions {
		if checkSelected(selected, definition.ID) {
			return true
		}
	}
	return false
}

func directiveItem(definition checkutil.Definition, value string, currentPolicy policy, now time.Time) task.CheckItem {
	compliant := directiveCompliant(definition.ID, value, currentPolicy)
	evidence := fmt.Sprintf("sshd -T without -C reported %s %s; active Include files for that default evaluation were parsed", directiveKey(definition.ID), value)
	if !compliant {
		return checkutil.Value(definition, value, false, evidence, now)
	}
	return checkutil.ManualReview(definition, value, defaultContextReason, evidence, now)
}

func sshConfigPermissionItem(raw string, now time.Time) task.CheckItem {
	parts := strings.Split(strings.TrimSpace(raw), "|")
	if len(parts) != 3 {
		return checkutil.Error(permissionDefinition, "sshd_config_stat_invalid", "stat did not return mode, owner and group", "Expected stat -c %a|%U|%G output", now)
	}
	modeText, owner, group := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), strings.TrimSpace(parts[2])
	mode, err := strconv.ParseUint(modeText, 8, 32)
	if err != nil {
		return checkutil.Error(permissionDefinition, "sshd_config_mode_invalid", err.Error(), "stat returned a non-octal mode", now)
	}
	compliant := owner == "root" && group == "root" && mode&0o077 == 0 && mode&0o111 == 0
	current := fmt.Sprintf("mode=%s owner=%s group=%s", modeText, owner, group)
	return checkutil.Value(permissionDefinition, current, compliant, "Read /etc/ssh/sshd_config mode, owner and group with stat -c %a|%U|%G", now)
}

func parseEffectiveConfig(contents string) map[string]string {
	values := map[string]string{}
	scanner := bufio.NewScanner(strings.NewReader(contents))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		values[strings.ToLower(fields[0])] = strings.Join(fields[1:], " ")
	}
	return values
}

func directiveKey(id string) string {
	return strings.TrimPrefix(strings.ReplaceAll(id, "_", ""), "ssh.")
}

func directiveCompliant(id, value string, currentPolicy policy) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	switch id {
	case "ssh.permit_empty_passwords", "ssh.x11_forwarding":
		return lower == "no"
	case "ssh.password_authentication":
		return lower == currentPolicy.PasswordAuthentication
	case "ssh.permit_root_login":
		return lower == currentPolicy.PermitRootLogin
	case "ssh.max_auth_tries":
		return integerBetween(lower, 1, 4)
	case "ssh.client_alive_interval":
		return integerBetween(lower, 1, 300)
	case "ssh.client_alive_count_max":
		return integerBetween(lower, 0, 3)
	case "ssh.port":
		return integerBetween(lower, 1, 65535)
	case "ssh.banner":
		return lower != "" && lower != "none"
	case "ssh.ciphers":
		for _, weak := range []string{"3des", "des-cbc3", "arcfour", "rc4", "-cbc"} {
			if strings.Contains(lower, weak) {
				return false
			}
		}
		return lower != ""
	default:
		return false
	}
}

func integerBetween(value string, minimum, maximum int) bool {
	number, err := strconv.Atoi(value)
	return err == nil && number >= minimum && number <= maximum
}

func boundedSummary(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > 240 {
		return value[:240] + "..."
	}
	return value
}
