package linuxfiles

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"setpoint/internal/executor"
	"setpoint/internal/plugin"
	"setpoint/internal/plugins/checkutil"
	"setpoint/internal/task"
)

const ID = "linux.files.permissions"

const wtmpGroupWriteParameter = "wtmp_group_write_policy"

type pathDefinition struct {
	checkutil.Definition
	path          string
	directory     bool
	optional      bool
	allowedGroups []string
	rule          string
}

var definitions = []pathDefinition{
	fileDefinition("permissions.group", "/etc/group", "root:root，0644 或更严格且不可被组/其他用户写入", []string{"root"}, "public_config", false),
	fileDefinition("permissions.gshadow", "/etc/gshadow", "root:root 或 root:shadow，0640 或更严格且其他用户无权限", []string{"root", "shadow"}, "restricted_config", false),
	fileDefinition("permissions.services", "/etc/services", "root:root，0644 或更严格且不可被组/其他用户写入", []string{"root"}, "public_config", false),
	fileDefinition("permissions.login_defs", "/etc/login.defs", "root:root，0644 或更严格且不可被组/其他用户写入", []string{"root"}, "public_config", false),
	directoryDefinition("permissions.security_directory", "/etc/security", "root:root 且不可被组/其他用户写入", "security_directory", false),
	directoryDefinition("permissions.cron_spool", "/var/spool/cron", "root:root，0700 或更严格", "cron_directory", true),
	fileDefinition("permissions.wtmp", "/var/log/wtmp", "root:root 或 root:utmp；默认禁止 group write，可由 Policy 放宽", []string{"root", "utmp"}, "wtmp", true),
}

func fileDefinition(id, path, recommended string, groups []string, rule string, optional bool) pathDefinition {
	return pathDefinition{
		Definition: checkutil.Definition{
			ID: id, Name: path + " 权限和属主", Recommended: recommended, Risk: "high",
			Description: "固定系统文件的类型、权限或属主异常可能破坏账号、策略或日志完整性。",
			Remediation: "确认发行版和业务约定后，通过受控变更恢复文件类型、mode、owner 和 group。",
			SourceRefs:  []string{"security-baseline:1.12"},
		},
		path: path, allowedGroups: groups, rule: rule, optional: optional,
	}
}

func directoryDefinition(id, path, recommended, rule string, optional bool) pathDefinition {
	current := fileDefinition(id, path, recommended, []string{"root"}, rule, optional)
	current.directory = true
	current.Description = "固定系统目录的类型、权限或属主异常可能允许非特权用户篡改安全配置或计划任务。"
	return current
}

type Plugin struct{}

func New() Plugin { return Plugin{} }

func (Plugin) Metadata() plugin.Metadata {
	checks := make([]plugin.CheckItemDefinition, 0, len(definitions))
	for _, current := range definitions {
		checks = append(checks, plugin.CheckItemDefinition{
			ID: current.ID, Name: current.Name, Description: current.Description,
			RecommendedValue: current.Recommended, SourceRefs: append([]string(nil), current.SourceRefs...),
		})
	}
	return plugin.Metadata{
		ID: ID, Category: "Linux", Name: "Linux 固定路径权限检查", Version: "1.0.0",
		Description: "只读检查七个 allowlist 系统路径的类型、mode、owner 和 group；拒绝跟随符号链接。",
		Mode:        plugin.ModeReadOnly, Risk: plugin.RiskLow,
		Impact:           "只执行固定参数的 test 和 stat，不遍历目录",
		SupportedSystems: []string{"linux"},
		Parameters: []plugin.Parameter{{
			Name: wtmpGroupWriteParameter, Type: "string",
			Description: "wtmp 是否允许批准的 utmp/root 组写入；默认 deny",
			Options:     []string{"deny", "allow"},
		}},
		Checks: checks,
	}
}

func (Plugin) Detect(context.Context, plugin.CheckInput) (plugin.Detection, error) {
	return plugin.Detection{Applicable: true}, nil
}

func (Plugin) Check(ctx context.Context, input plugin.CheckInput) ([]task.CheckItem, error) {
	now := time.Now().UTC()
	selected := selectedCheckSet(input.SelectedCheckIDs)
	parameters, parameterErr := decodeParameters(input.Parameters)
	if parameterErr != nil {
		return selectedErrors(selected, parameterErr, now), parameterErr
	}
	wtmpGroupWrite := false
	if checkSelected(selected, "permissions.wtmp") {
		var err error
		wtmpGroupWrite, err = parseWtmpPolicy(parameters)
		if err != nil {
			item := checkutil.Error(definitions[6].Definition, "invalid_check_parameters", err.Error(),
				"The wtmp permission policy was rejected before observation", now)
			return []task.CheckItem{item}, err
		}
	}

	items := make([]task.CheckItem, 0, len(definitions))
	var failures []error
	for _, current := range definitions {
		if !checkSelected(selected, current.ID) {
			continue
		}
		item, err := inspectPath(ctx, input.Executor, current, wtmpGroupWrite, now)
		items = append(items, item)
		if err != nil {
			failures = append(failures, err)
		}
	}
	return items, errors.Join(failures...)
}

func inspectPath(
	ctx context.Context,
	commandExecutor executor.CommandExecutor,
	current pathDefinition,
	wtmpGroupWrite bool,
	now time.Time,
) (task.CheckItem, error) {
	symlink, err := testPath(ctx, commandExecutor, "-L", current.path)
	if err != nil {
		return probeError(current, "path_symlink_probe_failed", err, "Unable to determine whether the fixed path is a symbolic link", now), err
	}
	if symlink {
		return checkutil.ManualReview(current.Definition, "symbolic link",
			"The fixed security path is a symbolic link; Setpoint refused to follow it without a separately approved target boundary",
			"test -L identified the allowlist path as a symbolic link; stat was not executed", now), nil
	}

	exists, err := testPath(ctx, commandExecutor, "-e", current.path)
	if err != nil {
		return probeError(current, "path_presence_probe_failed", err, "Unable to determine whether the fixed path exists", now), err
	}
	if !exists {
		if current.optional {
			return checkutil.NotApplicable(current.Definition, "The optional fixed path does not exist on this host", now), nil
		}
		return checkutil.ManualReview(current.Definition, "missing",
			"The required fixed path is absent; absence alone is not enough to infer the host's effective component or recovery state",
			"test -e reported the fixed path absent; no alternate path search was performed", now), nil
	}

	typeFlag := "-f"
	typeName := "regular file"
	if current.directory {
		typeFlag, typeName = "-d", "directory"
	}
	correctType, err := testPath(ctx, commandExecutor, typeFlag, current.path)
	if err != nil {
		return probeError(current, "path_type_probe_failed", err, "Unable to verify the fixed path type", now), err
	}
	if !correctType {
		return checkutil.Value(current.Definition, "unexpected path type", false,
			fmt.Sprintf("The fixed path exists but is not a %s; stat was not used to follow another object", typeName), now), nil
	}

	result, err := commandExecutor.Execute(ctx, executor.Command{
		Name: "stat", Args: []string{"-c", "%a|%U|%G", "--", current.path},
	})
	if err != nil {
		return probeError(current, checkutil.ErrorCode(err, "path_stat_failed"), err,
			"Unable to read mode, owner and group for the fixed path", now), err
	}
	if result.StdoutTruncated {
		err = errors.New("stat output exceeded the configured limit")
		return probeError(current, "path_stat_truncated", err, "stat output was truncated", now), err
	}
	metadata, err := parseMetadata(result.Stdout)
	if err != nil {
		return probeError(current, "path_stat_invalid", err, "stat did not return valid mode, owner and group", now), err
	}
	compliant := permissionCompliant(current, metadata, wtmpGroupWrite)
	value := fmt.Sprintf("mode=%s owner=%s group=%s type=%s", metadata.modeText, metadata.owner, metadata.group, typeName)
	return checkutil.Value(current.Definition, value, compliant,
		"Checked the allowlist path without following symlinks, then read mode, owner and group with stat", now), nil
}

type fileMetadata struct {
	mode     uint64
	modeText string
	owner    string
	group    string
}

func parseMetadata(raw string) (fileMetadata, error) {
	parts := strings.Split(strings.TrimSpace(raw), "|")
	if len(parts) != 3 {
		return fileMetadata{}, errors.New("expected mode, owner and group")
	}
	modeText := strings.TrimSpace(parts[0])
	mode, err := strconv.ParseUint(modeText, 8, 32)
	if err != nil || mode > 0o7777 {
		return fileMetadata{}, fmt.Errorf("invalid octal mode %q", modeText)
	}
	owner, group := strings.TrimSpace(parts[1]), strings.TrimSpace(parts[2])
	if owner == "" || group == "" {
		return fileMetadata{}, errors.New("owner and group are required")
	}
	return fileMetadata{mode: mode, modeText: modeText, owner: owner, group: group}, nil
}

func permissionCompliant(current pathDefinition, metadata fileMetadata, wtmpGroupWrite bool) bool {
	if metadata.owner != "root" || !contains(current.allowedGroups, metadata.group) || metadata.mode&0o7000 != 0 {
		return false
	}
	switch current.rule {
	case "public_config":
		return metadata.mode&0o600 == 0o600 && metadata.mode&0o133 == 0
	case "restricted_config":
		return metadata.mode&0o600 == 0o600 && metadata.mode&0o137 == 0
	case "security_directory":
		return metadata.mode&0o500 == 0o500 && metadata.mode&0o022 == 0
	case "cron_directory":
		return metadata.mode&0o700 == 0o700 && metadata.mode&0o077 == 0
	case "wtmp":
		if metadata.mode&0o600 != 0o600 || metadata.mode&0o113 != 0 {
			return false
		}
		return wtmpGroupWrite || metadata.mode&0o020 == 0
	default:
		return false
	}
}

func testPath(ctx context.Context, commandExecutor executor.CommandExecutor, flag, path string) (bool, error) {
	result, err := commandExecutor.Execute(ctx, executor.Command{Name: "test", Args: []string{flag, path}})
	if err == nil {
		return true, nil
	}
	var executionError *executor.Error
	if errors.As(err, &executionError) && executionError.Kind == executor.ErrorExit && result.ExitCode == 1 {
		return false, nil
	}
	return false, err
}

func probeError(current pathDefinition, code string, err error, evidence string, now time.Time) task.CheckItem {
	return checkutil.Error(current.Definition, code, err.Error(), evidence, now)
}

func decodeParameters(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" || string(raw) == "{}" {
		return map[string]json.RawMessage{}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var values map[string]json.RawMessage
	if err := decoder.Decode(&values); err != nil || values == nil {
		return nil, fmt.Errorf("decode Linux file permission parameters: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("decode Linux file permission parameters: trailing JSON value")
	}
	for name := range values {
		if name != wtmpGroupWriteParameter {
			return nil, fmt.Errorf("unknown Linux file permission parameter %q", name)
		}
	}
	return values, nil
}

func parseWtmpPolicy(parameters map[string]json.RawMessage) (bool, error) {
	raw, exists := parameters[wtmpGroupWriteParameter]
	if !exists {
		return false, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, errors.New("wtmp_group_write_policy must be a string")
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "deny":
		return false, nil
	case "allow":
		return true, nil
	default:
		return false, fmt.Errorf("unsupported wtmp_group_write_policy %q", value)
	}
}

func selectedErrors(selected map[string]struct{}, err error, now time.Time) []task.CheckItem {
	items := make([]task.CheckItem, 0, len(definitions))
	for _, current := range definitions {
		if checkSelected(selected, current.ID) {
			items = append(items, checkutil.Error(current.Definition, "invalid_check_parameters", err.Error(),
				"Linux file permission parameters were rejected before observation", now))
		}
	}
	return items
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
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
