package linuxaudit

import (
	"bufio"
	"context"
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

const ID = "linux.audit.observation"

var definitions = []checkutil.Definition{
	{ID: "audit.service.auditd", Name: "auditd 服务状态", Recommended: "loaded 且 active", Risk: "high", Description: "缺少活动的审计服务会削弱安全事件追踪能力。", Remediation: "确认发行版审计方案和规则后，通过受控变更启用并验证 auditd。", SourceRefs: []string{"d9d10:module-46"}},
	{ID: "audit.service.rsyslog", Name: "rsyslog 服务状态", Recommended: "活动的系统日志服务", Risk: "medium", Description: "系统日志服务不可用可能造成运行和安全事件缺少持久记录。", Remediation: "先确认系统实际使用的日志实现，再通过受控变更恢复服务。", SourceRefs: []string{"d9d10:module-46"}},
	{ID: "audit.log.directory_permissions", Name: "审计日志目录权限", Recommended: "root:root 且 0750 或更严格", Risk: "high", Description: "审计目录可被非特权用户写入会破坏证据完整性。", Remediation: "确认审计组件的属主和组约定后收紧目录权限。", SourceRefs: []string{"d9d10:module-47", "security-baseline:1.12"}},
	{ID: "audit.log.file_permissions", Name: "当前审计日志权限", Recommended: "root:root 且 0640 或更严格", Risk: "high", Description: "审计日志可被非特权用户修改或读取会损害完整性或泄露敏感事件。", Remediation: "确认审计日志轮转和读取账号后收紧文件权限。", SourceRefs: []string{"d9d10:module-47", "security-baseline:1.12"}},
	{ID: "account.empty_password_hashes", Name: "本地账号空口令字段", Recommended: "0 个空口令字段", Risk: "critical", Description: "空口令字段可能允许绕过预期的口令验证。", Remediation: "逐账号确认用途和认证链路后锁定或设置受控凭据；本检查不返回账号名或哈希。", SourceRefs: []string{"d9d10:module-40"}, MayAffectConnection: true},
	{ID: "account.duplicate_uids", Name: "本地账号重复 UID", Recommended: "0 个重复 UID", Risk: "high", Description: "多个账号共享 UID 会削弱身份归属和审计可追踪性。", Remediation: "确认业务账号依赖后，人工规划唯一 UID 迁移。", SourceRefs: []string{"d9d10:module-40", "security-baseline:1.9"}, MayAffectConnection: true},
	{ID: "service.listening_inventory", Name: "监听端点只读清单", Recommended: "按项目批准清单人工复核", Risk: "medium", Description: "监听面是否必要依赖业务和网络边界，不能使用通用黑白名单自动下结论。", Remediation: "将汇总与项目批准的服务清单比对；端口和防火墙变更必须走独立受控流程。", SourceRefs: []string{"d9d10:module-48"}, MayAffectConnection: true, MayAffectBusiness: true},
	{ID: "service.enabled_inventory", Name: "启用服务最小化清单", Recommended: "按项目最小安装清单人工复核", Risk: "medium", Description: "最小安装范围依赖主机角色，启用服务数量本身不能证明安全或不安全。", Remediation: "按主机角色和项目批准清单逐项复核；本检查不停止服务。", SourceRefs: []string{"d9d10:module-49"}, MayAffectBusiness: true},
}

type Plugin struct{}

func New() Plugin { return Plugin{} }

func (Plugin) Metadata() plugin.Metadata {
	checks := make([]plugin.CheckItemDefinition, 0, len(definitions))
	for _, definition := range definitions {
		checks = append(checks, plugin.CheckItemDefinition{
			ID: definition.ID, Name: definition.Name, Description: definition.Description,
			RecommendedValue: definition.Recommended, SourceRefs: append([]string(nil), definition.SourceRefs...),
		})
	}
	return plugin.Metadata{
		ID: ID, Category: "审计与日志", Name: "Linux 审计、账号与服务观察", Version: "1.0.0",
		Description: "只读检查审计/日志服务、审计文件权限和账号异常，并输出不含地址、账号名或密码哈希的服务面汇总。",
		Mode:        plugin.ModeReadOnly, Risk: plugin.RiskLow, Impact: "只读执行固定的 systemctl、test、stat、cat 和 ss 命令",
		SupportedSystems: []string{"linux"}, Parameters: []plugin.Parameter{}, Checks: checks,
	}
}

func (Plugin) Detect(context.Context, plugin.CheckInput) (plugin.Detection, error) {
	return plugin.Detection{Applicable: true}, nil
}

func (Plugin) Check(ctx context.Context, input plugin.CheckInput) ([]task.CheckItem, error) {
	selected := selectedIDs(input.SelectedCheckIDs)
	items := make([]task.CheckItem, 0, len(definitions))
	var failures []error
	for _, definition := range definitions {
		if len(selected) > 0 {
			if _, exists := selected[definition.ID]; !exists {
				continue
			}
		}
		var item task.CheckItem
		var err error
		switch definition.ID {
		case "audit.service.auditd":
			item, err = serviceStateItem(ctx, input.Executor, definition, "auditd.service", false)
		case "audit.service.rsyslog":
			item, err = serviceStateItem(ctx, input.Executor, definition, "rsyslog.service", true)
		case "audit.log.directory_permissions":
			item, err = permissionItem(ctx, input.Executor, definition, "/var/log/audit", true)
		case "audit.log.file_permissions":
			item, err = optionalFilePermissionItem(ctx, input.Executor, definition, "/var/log/audit/audit.log")
		case "account.empty_password_hashes":
			item, err = emptyPasswordItem(ctx, input.Executor, definition)
		case "account.duplicate_uids":
			item, err = duplicateUIDItem(ctx, input.Executor, definition)
		case "service.listening_inventory":
			item, err = listenerInventoryItem(ctx, input.Executor, definition)
		case "service.enabled_inventory":
			item, err = enabledServiceInventoryItem(ctx, input.Executor, definition)
		default:
			err = fmt.Errorf("unsupported audit check %s", definition.ID)
			item = checkutil.Error(definition, "audit_check_unsupported", err.Error(), "No observation was executed", time.Now().UTC())
		}
		items = append(items, item)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", definition.ID, err))
		}
	}
	return items, errors.Join(failures...)
}

func serviceStateItem(
	ctx context.Context,
	commandExecutor executor.CommandExecutor,
	definition checkutil.Definition,
	unit string,
	alternativeAllowed bool,
) (task.CheckItem, error) {
	result, executeErr := commandExecutor.Execute(ctx, executor.Command{
		Name: "systemctl", Args: []string{"show", unit, "--property=LoadState", "--property=ActiveState", "--no-pager"},
	})
	now := time.Now().UTC()
	if result.StdoutTruncated || result.StderrTruncated {
		err := errors.New("systemctl output exceeded the configured limit")
		return checkutil.Error(definition, "service_state_truncated", err.Error(), "Service state output was truncated", now), err
	}
	values := parseAssignments(result.Stdout)
	loadState, activeState := values["LoadState"], values["ActiveState"]
	current := fmt.Sprintf("load=%s active=%s", valueOrUnknown(loadState), valueOrUnknown(activeState))
	if loadState == "not-found" && alternativeAllowed {
		return checkutil.ManualReview(definition, current,
			"The rsyslog unit was not found; an alternative logging implementation may be in use",
			"systemctl reported the unit load and active states", now), nil
	}
	if loadState != "" && activeState != "" {
		return checkutil.Value(definition, current, loadState == "loaded" && activeState == "active",
			"systemctl reported the unit load and active states without starting or stopping it", now), nil
	}
	if executeErr != nil {
		return checkutil.Error(definition, checkutil.ErrorCode(executeErr, "service_state_failed"), executeErr.Error(),
			"Unable to obtain structured unit state", now), executeErr
	}
	err := errors.New("systemctl returned no structured unit state")
	return checkutil.Error(definition, "service_state_invalid", err.Error(), "Unit state fields were absent", now), err
}

type fileMetadata struct {
	mode, owner, group string
}

func optionalFilePermissionItem(
	ctx context.Context,
	commandExecutor executor.CommandExecutor,
	definition checkutil.Definition,
	path string,
) (task.CheckItem, error) {
	result, err := commandExecutor.Execute(ctx, executor.Command{Name: "test", Args: []string{"-e", path}})
	if err == nil {
		return permissionItem(ctx, commandExecutor, definition, path, false)
	}
	var executionError *executor.Error
	if errors.As(err, &executionError) && executionError.Kind == executor.ErrorExit && result.ExitCode == 1 {
		return checkutil.NotApplicable(definition,
			"The fixed current audit log path does not exist", time.Now().UTC()), nil
	}
	return checkutil.Error(definition, checkutil.ErrorCode(err, "audit_path_presence_failed"), err.Error(),
		"Unable to determine whether the fixed current audit log path exists", time.Now().UTC()), err
}

func permissionItem(
	ctx context.Context,
	commandExecutor executor.CommandExecutor,
	definition checkutil.Definition,
	path string,
	directory bool,
) (task.CheckItem, error) {
	result, err := commandExecutor.Execute(ctx, executor.Command{Name: "stat", Args: []string{"-c", "%a|%U|%G", "--", path}})
	now := time.Now().UTC()
	if err != nil {
		return checkutil.Error(definition, checkutil.ErrorCode(err, "audit_path_stat_failed"), err.Error(),
			"Unable to read the fixed audit path metadata", now), err
	}
	parts := strings.Split(strings.TrimSpace(result.Stdout), "|")
	if len(parts) != 3 {
		err := errors.New("stat did not return mode, owner and group")
		return checkutil.Error(definition, "audit_path_stat_invalid", err.Error(), "Invalid stat output", now), err
	}
	metadata := fileMetadata{mode: strings.TrimSpace(parts[0]), owner: strings.TrimSpace(parts[1]), group: strings.TrimSpace(parts[2])}
	mode, parseErr := strconv.ParseUint(metadata.mode, 8, 32)
	if parseErr != nil {
		return checkutil.Error(definition, "audit_path_mode_invalid", parseErr.Error(), "stat returned a non-octal mode", now), parseErr
	}
	compliant := metadata.owner == "root" && metadata.group == "root"
	if directory {
		compliant = compliant && mode&0o027 == 0
	} else {
		compliant = compliant && mode&0o137 == 0
	}
	current := fmt.Sprintf("mode=%s owner=%s group=%s", metadata.mode, metadata.owner, metadata.group)
	return checkutil.Value(definition, current, compliant, "Read mode, owner and group from the fixed audit path", now), nil
}

func emptyPasswordItem(
	ctx context.Context,
	commandExecutor executor.CommandExecutor,
	definition checkutil.Definition,
) (task.CheckItem, error) {
	contents, err := readFixedFile(ctx, commandExecutor, "/etc/shadow")
	now := time.Now().UTC()
	if err != nil {
		return checkutil.Error(definition, checkutil.ErrorCode(err, "shadow_read_failed"), err.Error(),
			"Unable to inspect local password fields", now), err
	}
	empty, malformed, records := 0, 0, 0
	for _, line := range strings.Split(contents, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		records++
		fields := strings.Split(line, ":")
		if len(fields) < 2 {
			malformed++
			continue
		}
		if fields[1] == "" {
			empty++
		}
	}
	current := fmt.Sprintf("empty_fields=%d records=%d", empty, records)
	if malformed > 0 {
		return checkutil.ManualReview(definition, current,
			"One or more local account records could not be parsed, so a safe conclusion is not possible",
			"Parsed password-field presence only; account names and password hashes were not retained", now), nil
	}
	return checkutil.Value(definition, current, empty == 0,
		"Parsed password-field presence only; account names and password hashes were not retained", now), nil
}

func duplicateUIDItem(
	ctx context.Context,
	commandExecutor executor.CommandExecutor,
	definition checkutil.Definition,
) (task.CheckItem, error) {
	contents, err := readFixedFile(ctx, commandExecutor, "/etc/passwd")
	now := time.Now().UTC()
	if err != nil {
		return checkutil.Error(definition, checkutil.ErrorCode(err, "passwd_read_failed"), err.Error(),
			"Unable to inspect local UID fields", now), err
	}
	counts := make(map[string]int)
	malformed, records := 0, 0
	for _, line := range strings.Split(contents, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		records++
		fields := strings.Split(line, ":")
		if len(fields) < 3 {
			malformed++
			continue
		}
		if _, parseErr := strconv.ParseUint(fields[2], 10, 32); parseErr != nil {
			malformed++
			continue
		}
		counts[fields[2]]++
	}
	duplicates := 0
	for _, count := range counts {
		if count > 1 {
			duplicates++
		}
	}
	current := fmt.Sprintf("duplicate_uids=%d records=%d", duplicates, records)
	if malformed > 0 {
		return checkutil.ManualReview(definition, current,
			"One or more local account records could not be parsed, so UID uniqueness cannot be proven",
			"Parsed numeric UID fields only; account names were not retained", now), nil
	}
	return checkutil.Value(definition, current, duplicates == 0,
		"Parsed numeric UID fields only; account names were not retained", now), nil
}

func listenerInventoryItem(
	ctx context.Context,
	commandExecutor executor.CommandExecutor,
	definition checkutil.Definition,
) (task.CheckItem, error) {
	result, err := commandExecutor.Execute(ctx, executor.Command{Name: "ss", Args: []string{"-lntuH"}})
	now := time.Now().UTC()
	if err != nil {
		return checkutil.Error(definition, checkutil.ErrorCode(err, "listener_inventory_failed"), err.Error(),
			"Unable to collect the listener inventory", now), err
	}
	if result.StdoutTruncated {
		err := errors.New("listener inventory exceeded the configured output limit")
		return checkutil.Error(definition, "listener_inventory_truncated", err.Error(), "Listener output was truncated", now), err
	}
	tcp, udp, total := 0, 0, 0
	scanner := bufio.NewScanner(strings.NewReader(result.Stdout))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 {
			continue
		}
		total++
		switch strings.ToLower(fields[0]) {
		case "tcp":
			tcp++
		case "udp":
			udp++
		}
	}
	return checkutil.ManualReview(definition, fmt.Sprintf("listeners=%d tcp=%d udp=%d", total, tcp, udp),
		"Listener necessity depends on the host role and approved network exposure",
		"Counted listener rows only; addresses and process details were not retained", now), nil
}

func enabledServiceInventoryItem(
	ctx context.Context,
	commandExecutor executor.CommandExecutor,
	definition checkutil.Definition,
) (task.CheckItem, error) {
	result, err := commandExecutor.Execute(ctx, executor.Command{
		Name: "systemctl", Args: []string{"list-unit-files", "--type=service", "--state=enabled", "--no-legend", "--no-pager"},
	})
	now := time.Now().UTC()
	if err != nil {
		return checkutil.Error(definition, checkutil.ErrorCode(err, "enabled_service_inventory_failed"), err.Error(),
			"Unable to collect enabled service units", now), err
	}
	if result.StdoutTruncated {
		err := errors.New("enabled service inventory exceeded the configured output limit")
		return checkutil.Error(definition, "enabled_service_inventory_truncated", err.Error(), "Service output was truncated", now), err
	}
	count := 0
	for _, line := range strings.Split(result.Stdout, "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return checkutil.ManualReview(definition, fmt.Sprintf("enabled_service_units=%d", count),
		"The minimum required service set depends on the approved host role",
		"Counted enabled service unit rows only; unit names were not retained", now), nil
}

func readFixedFile(ctx context.Context, commandExecutor executor.CommandExecutor, path string) (string, error) {
	result, err := commandExecutor.Execute(ctx, executor.Command{Name: "cat", Args: []string{"--", path}})
	if err != nil {
		return "", err
	}
	if result.StdoutTruncated {
		return "", errors.New("fixed file output exceeded the configured limit")
	}
	return result.Stdout, nil
}

func parseAssignments(contents string) map[string]string {
	values := make(map[string]string)
	for _, line := range strings.Split(contents, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "=", 2)
		if len(parts) == 2 {
			values[parts[0]] = parts[1]
		}
	}
	return values
}

func selectedIDs(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func valueOrUnknown(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return value
}
