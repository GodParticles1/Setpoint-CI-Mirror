package linuxbaseline

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"setpoint/internal/executor"
	"setpoint/internal/plugin"
	"setpoint/internal/plugins/checkutil"
	"setpoint/internal/task"
)

const ID = "linux.baseline.core"

var definitions = append([]checkutil.Definition{
	{ID: "shell.tmout", Name: "Shell 会话超时", Recommended: "1-900 秒", Risk: "medium", Description: "长期空闲会话会增加未授权使用风险；最终值可能受其他系统级和用户级 Shell 配置影响。", Remediation: "经现场确认后在系统级 Shell 配置中设置只读且不超过 900 秒的 TMOUT。"},
	{ID: "shell.umask", Name: "默认 umask", Recommended: "027 或更严格", Risk: "medium", Description: "宽松的默认权限可能暴露新建文件；最终值可能受登录方式、Shell 和用户配置影响。", Remediation: "经业务兼容性确认后设置系统级 umask 为 027 或 077。"},
	{ID: "shell.histsize", Name: "Shell 历史记录数量", Recommended: ">= 1000", Risk: "low", Description: "过短的命令历史不利于问题追踪；最终值可能受其他 Shell 配置影响。", Remediation: "在系统级 Shell 配置中设置 HISTSIZE 至少为 1000。"},
	{ID: "login.banner", Name: "系统登录 Banner", Recommended: "包含授权访问警示", Risk: "low", Description: "缺少访问警示会削弱登录前的安全告知。", Remediation: "按公司和客户批准文本维护 /etc/issue；本检查不写入 Banner。"},
	{ID: "password.max_days", Name: "login.defs 密码最长有效期默认值", Recommended: "1-90 天", Risk: "medium", Description: "PASS_MAX_DAYS 只描述创建账号时的默认值，不证明已有账号当前策略。", Remediation: "确认账号策略后调整 PASS_MAX_DAYS；已有账号需另行评估。", MayAffectConnection: true},
	{ID: "password.min_days", Name: "login.defs 密码最短使用期默认值", Recommended: ">= 1 天", Risk: "medium", Description: "PASS_MIN_DAYS 只描述创建账号时的默认值，不证明已有账号当前策略。", Remediation: "确认账号策略后调整 PASS_MIN_DAYS；已有账号需另行评估。", MayAffectConnection: true},
	{ID: "password.min_length", Name: "login.defs PASS_MIN_LEN 默认值", Recommended: ">= 8（仅默认值，需另查 PAM/pwquality）", Risk: "medium", Description: "PASS_MIN_LEN 不是 PAM/pwquality 实际生效密码长度的充分证据。", Remediation: "结合 PAM/pwquality 实际认证链路确认后调整密码最小长度。", MayAffectConnection: true},
	{ID: "permissions.shadow", Name: "/etc/shadow 权限和属主", Recommended: "root:root 或 root:shadow，0640 或更严格且其他用户无权限", Risk: "high", Description: "shadow 权限或属主异常可能泄露密码哈希。", Remediation: "确认发行版账号组约定后收紧权限、所有者和组。"},
	{ID: "permissions.passwd", Name: "/etc/passwd 权限和属主", Recommended: "root:root，0644 或更严格且不可被组/其他用户写入", Risk: "high", Description: "passwd 属主异常或可被非特权用户写入会破坏账号完整性。", Remediation: "确认发行版约定后恢复 root:root 并移除组和其他用户写权限。"},
}, checkutil.Definition{
	ID: "login.motd", Name: "系统 MOTD 授权警示", Recommended: "/etc/motd 包含明确的授权访问或禁止未授权访问警示",
	Risk: "low", Description: "空白或仅含系统标识的 MOTD 不能形成登录后的授权访问告知。",
	Remediation: "按公司和客户批准文本维护 /etc/motd；本检查不写入 MOTD。",
})

type Plugin struct{}

func New() Plugin { return Plugin{} }

func (Plugin) Metadata() plugin.Metadata {
	checks := make([]plugin.CheckItemDefinition, 0, len(definitions))
	for _, definition := range definitions {
		checks = append(checks, plugin.CheckItemDefinition{
			ID: definition.ID, Name: definition.Name, Description: definition.Description,
			RecommendedValue: definition.Recommended, SourceRefs: sourceRefs(definition.ID),
		})
	}
	return plugin.Metadata{
		ID: ID, Category: "Linux", Name: "Linux 基础安全检查", Version: "2.2.0",
		Description: "只读检查固定系统文件中的 Shell 观察值、登录提示、MOTD、login.defs 默认值和关键账号文件的 mode/owner/group。",
		Mode:        plugin.ModeReadOnly, Risk: plugin.RiskLow, Impact: "只读读取固定系统文件和权限元数据",
		SupportedSystems: []string{"linux"}, Parameters: []plugin.Parameter{}, Checks: checks,
	}
}

func (Plugin) Detect(context.Context, plugin.CheckInput) (plugin.Detection, error) {
	return plugin.Detection{Applicable: true}, nil
}

func (Plugin) Check(ctx context.Context, input plugin.CheckInput) ([]task.CheckItem, error) {
	now := time.Now().UTC()
	selected := selectedCheckSet(input.SelectedCheckIDs)
	items := make([]task.CheckItem, 0, len(definitions))
	var failures []error

	var profileErr, loginErr, bannerErr, motdErr, shadowErr, passwdErr error
	profileValues := map[string]string{}
	if anyCheckSelected(selected, "shell.tmout", "shell.umask", "shell.histsize") {
		profile, err := readFixedFile(ctx, input.Executor, "/etc/profile")
		profileErr = err
		profileValues = parseShellProfile(profile)
		if err != nil {
			failures = append(failures, err)
		}
	}

	var banner string
	if checkSelected(selected, definitions[3].ID) {
		banner, bannerErr = readFixedFile(ctx, input.Executor, "/etc/issue")
		if bannerErr != nil {
			failures = append(failures, bannerErr)
		}
	}
	var motd string
	motdExists := false
	if checkSelected(selected, definitions[9].ID) {
		motdExists, motdErr = fixedPathExists(ctx, input.Executor, "/etc/motd")
		if motdErr == nil && motdExists {
			motd, motdErr = readFixedFile(ctx, input.Executor, "/etc/motd")
		}
		if motdErr != nil {
			failures = append(failures, motdErr)
		}
	}

	loginValues := map[string]string{}
	if anyCheckSelected(selected, "password.max_days", "password.min_days", "password.min_length") {
		loginDefs, err := readFixedFile(ctx, input.Executor, "/etc/login.defs")
		loginErr = err
		loginValues = parseLoginDefs(loginDefs)
		if err != nil {
			failures = append(failures, err)
		}
	}

	var shadow, passwd fileMetadata
	if checkSelected(selected, definitions[7].ID) {
		shadow, shadowErr = statFile(ctx, input.Executor, "/etc/shadow")
		if shadowErr != nil {
			failures = append(failures, shadowErr)
		}
	}
	if checkSelected(selected, definitions[8].ID) {
		passwd, passwdErr = statFile(ctx, input.Executor, "/etc/passwd")
		if passwdErr != nil {
			failures = append(failures, passwdErr)
		}
	}

	for index, definition := range definitions {
		if !checkSelected(selected, definition.ID) {
			continue
		}
		switch index {
		case 0, 1, 2:
			if profileErr != nil {
				items = append(items, checkutil.Error(definition, checkutil.ErrorCode(profileErr, "profile_read_failed"), profileErr.Error(), "Unable to read /etc/profile", now))
				continue
			}
			value := valueOrMissing(profileValues[definition.ID])
			items = append(items, checkutil.ManualReview(definition, value,
				"Only /etc/profile was parsed; other system and user Shell sources may override the final effective value",
				fmt.Sprintf("Parsed %s only from /etc/profile; no directory or user-file search was performed", definition.ID), now))
		case 3:
			if bannerErr != nil {
				items = append(items, checkutil.Error(definition, checkutil.ErrorCode(bannerErr, "banner_read_failed"), bannerErr.Error(), "Unable to read /etc/issue", now))
				continue
			}
			current := summarizeBanner(banner)
			lower := strings.ToLower(banner)
			compliant := strings.Contains(lower, "authoriz") || strings.Contains(banner, "授权") || strings.Contains(banner, "禁止")
			items = append(items, checkutil.Value(definition, valueOrMissing(current), compliant, "Read the bounded /etc/issue content", now))
		case 4, 5, 6:
			if loginErr != nil {
				items = append(items, checkutil.Error(definition, checkutil.ErrorCode(loginErr, "login_defs_read_failed"), loginErr.Error(), "Unable to read /etc/login.defs", now))
				continue
			}
			value := loginValues[definition.ID]
			if strings.TrimSpace(value) == "" {
				items = append(items, checkutil.ManualReview(definition, "not explicitly configured",
					"The directive is absent from /etc/login.defs and the effective default cannot be proven from this file",
					fmt.Sprintf("%s was not present in /etc/login.defs", loginDefsKey(definition.ID)), now))
				continue
			}
			evidence := fmt.Sprintf("Parsed %s from /etc/login.defs; this does not verify existing account state", loginDefsKey(definition.ID))
			if definition.ID == "password.min_length" {
				evidence += " or the PAM/pwquality authentication chain"
			}
			items = append(items, checkutil.Value(definition, value, loginValueCompliant(definition.ID, value), evidence, now))
		case 7:
			items = append(items, permissionItem(definition, shadow, shadowErr, true, now))
		case 8:
			items = append(items, permissionItem(definition, passwd, passwdErr, false, now))
		case 9:
			if motdErr != nil {
				items = append(items, checkutil.Error(definition, checkutil.ErrorCode(motdErr, "motd_read_failed"), motdErr.Error(), "Unable to read /etc/motd", now))
				continue
			}
			if !motdExists {
				items = append(items, checkutil.Value(definition, "not present", false,
					"A fixed-path existence probe found no /etc/motd file", now))
				continue
			}
			compliant := motdAuthorizedWarning(motd)
			current := "content present without a recognized authorization warning"
			if strings.TrimSpace(motd) == "" {
				current = "empty"
			} else if compliant {
				current = "authorization warning detected"
			}
			items = append(items, checkutil.Value(definition, current, compliant,
				"Read bounded /etc/motd content without merging /etc/issue or SSH Banner", now))
		}
	}
	return items, errors.Join(failures...)
}

func motdAuthorizedWarning(value string) bool {
	lower := strings.ToLower(value)
	english := strings.Contains(lower, "authorized access only") ||
		strings.Contains(lower, "unauthoriz") &&
			(strings.Contains(lower, "prohibit") || strings.Contains(lower, "forbid") || strings.Contains(lower, "denied"))
	chinese := strings.Contains(value, "仅限授权") || strings.Contains(value, "授权访问") ||
		(strings.Contains(value, "未授权") || strings.Contains(value, "非授权")) &&
			(strings.Contains(value, "禁止") || strings.Contains(value, "不得"))
	return english || chinese
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

func anyCheckSelected(selected map[string]struct{}, ids ...string) bool {
	for _, id := range ids {
		if checkSelected(selected, id) {
			return true
		}
	}
	return false
}

func readFixedFile(ctx context.Context, commandExecutor executor.CommandExecutor, path string) (string, error) {
	result, err := commandExecutor.Execute(ctx, executor.Command{Name: "cat", Args: []string{"--", path}})
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	if result.StdoutTruncated {
		return "", fmt.Errorf("read %s: output exceeded the configured limit", path)
	}
	return result.Stdout, nil
}

func fixedPathExists(ctx context.Context, commandExecutor executor.CommandExecutor, path string) (bool, error) {
	result, err := commandExecutor.Execute(ctx, executor.Command{Name: "test", Args: []string{"-e", path}})
	if err == nil {
		return true, nil
	}
	var executionError *executor.Error
	if errors.As(err, &executionError) && executionError.Kind == executor.ErrorExit && result.ExitCode == 1 {
		return false, nil
	}
	return false, fmt.Errorf("test %s existence: %w", path, err)
}

type fileMetadata struct {
	Mode  string
	Owner string
	Group string
}

func statFile(ctx context.Context, commandExecutor executor.CommandExecutor, path string) (fileMetadata, error) {
	result, err := commandExecutor.Execute(ctx, executor.Command{Name: "stat", Args: []string{"-c", "%a|%U|%G", "--", path}})
	if err != nil {
		return fileMetadata{}, fmt.Errorf("stat %s: %w", path, err)
	}
	parts := strings.Split(strings.TrimSpace(result.Stdout), "|")
	if len(parts) != 3 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" || strings.TrimSpace(parts[2]) == "" {
		return fileMetadata{}, fmt.Errorf("stat %s: expected mode, owner and group", path)
	}
	return fileMetadata{Mode: strings.TrimSpace(parts[0]), Owner: strings.TrimSpace(parts[1]), Group: strings.TrimSpace(parts[2])}, nil
}

var assignmentPattern = regexp.MustCompile(`^(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=\s*["']?([^"'\s;#]+)`)
var umaskPattern = regexp.MustCompile(`^umask\s+([0-7]{3,4})(?:\s|$)`)

func parseShellProfile(contents string) map[string]string {
	values := map[string]string{}
	scanner := bufio.NewScanner(strings.NewReader(contents))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if match := assignmentPattern.FindStringSubmatch(line); len(match) == 3 {
			switch strings.ToUpper(match[1]) {
			case "TMOUT":
				values["shell.tmout"] = match[2]
			case "HISTSIZE":
				values["shell.histsize"] = match[2]
			}
		}
		if match := umaskPattern.FindStringSubmatch(line); len(match) == 2 {
			values["shell.umask"] = match[1]
		}
	}
	return values
}

func parseLoginDefs(contents string) map[string]string {
	values := map[string]string{}
	mapping := map[string]string{
		"PASS_MAX_DAYS": "password.max_days", "PASS_MIN_DAYS": "password.min_days", "PASS_MIN_LEN": "password.min_length",
	}
	scanner := bufio.NewScanner(strings.NewReader(contents))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			if id, exists := mapping[strings.ToUpper(fields[0])]; exists {
				values[id] = fields[1]
			}
		}
	}
	return values
}

func loginDefsKey(id string) string {
	switch id {
	case "password.max_days":
		return "PASS_MAX_DAYS"
	case "password.min_days":
		return "PASS_MIN_DAYS"
	case "password.min_length":
		return "PASS_MIN_LEN"
	default:
		return id
	}
}

func loginValueCompliant(id, value string) bool {
	number, err := strconv.Atoi(value)
	if err != nil {
		return false
	}
	switch id {
	case "password.max_days":
		return number >= 1 && number <= 90
	case "password.min_days":
		return number >= 1
	case "password.min_length":
		return number >= 8
	default:
		return false
	}
}

func permissionItem(definition checkutil.Definition, metadata fileMetadata, err error, shadow bool, now time.Time) task.CheckItem {
	if err != nil {
		return checkutil.Error(definition, checkutil.ErrorCode(err, "file_stat_failed"), err.Error(), "Unable to read file mode, owner and group", now)
	}
	mode, parseErr := strconv.ParseUint(metadata.Mode, 8, 32)
	if parseErr != nil {
		return checkutil.Error(definition, "file_mode_invalid", parseErr.Error(), "stat returned a non-octal mode", now)
	}
	compliant := metadata.Owner == "root" && mode&0o111 == 0
	if shadow {
		compliant = compliant && (metadata.Group == "root" || metadata.Group == "shadow") && mode&0o027 == 0
	} else {
		compliant = compliant && metadata.Group == "root" && mode&0o022 == 0
	}
	current := fmt.Sprintf("mode=%s owner=%s group=%s", metadata.Mode, metadata.Owner, metadata.Group)
	return checkutil.Value(definition, current, compliant, "Read mode, owner and group with stat -c %a|%U|%G", now)
}

func valueOrMissing(value string) string {
	if strings.TrimSpace(value) == "" {
		return "not explicitly configured"
	}
	return strings.TrimSpace(value)
}

func summarizeBanner(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > 160 {
		return value[:160] + "..."
	}
	return value
}
