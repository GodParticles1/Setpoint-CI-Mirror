package linuxpassword

import (
	"bufio"
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

const ID = "linux.password.policy"

const (
	pwqualityPath = "/etc/security/pwquality.conf"
	loginDefsPath = "/etc/login.defs"
)

type policyDefinition struct {
	checkutil.Definition
	directive     string
	parameterName string
	defaultTarget int
	minimumTarget int
	maximumTarget int
	pwquality     bool
}

var definitions = []policyDefinition{
	{Definition: definition("password.pwquality.min_length", "pwquality 最小密码长度声明", "Setpoint 建议：>= 8", "minlen"), directive: "minlen", parameterName: "pwquality_min_length_target", defaultTarget: 8, minimumTarget: 8, maximumTarget: 128, pwquality: true},
	{Definition: definition("password.pwquality.digit_credit", "pwquality 数字字符要求声明", "Setpoint 建议：dcredit <= -1", "dcredit"), directive: "dcredit", parameterName: "pwquality_digit_credit_target", defaultTarget: -1, minimumTarget: -16, maximumTarget: 0, pwquality: true},
	{Definition: definition("password.pwquality.uppercase_credit", "pwquality 大写字符要求声明", "Setpoint 建议：ucredit <= -1", "ucredit"), directive: "ucredit", parameterName: "pwquality_uppercase_credit_target", defaultTarget: -1, minimumTarget: -16, maximumTarget: 0, pwquality: true},
	{Definition: definition("password.pwquality.lowercase_credit", "pwquality 小写字符要求声明", "Setpoint 建议：lcredit <= -1", "lcredit"), directive: "lcredit", parameterName: "pwquality_lowercase_credit_target", defaultTarget: -1, minimumTarget: -16, maximumTarget: 0, pwquality: true},
	{Definition: definition("password.pwquality.other_credit", "pwquality 其他字符要求声明", "Setpoint 建议：ocredit <= -1", "ocredit"), directive: "ocredit", parameterName: "pwquality_other_credit_target", defaultTarget: -1, minimumTarget: -16, maximumTarget: 0, pwquality: true},
	{
		Definition: checkutil.Definition{
			ID: "password.warn_days", Name: "login.defs 密码到期警告默认值",
			Recommended: "Setpoint 建议：PASS_WARN_AGE >= 7 天", Risk: "medium",
			Description:         "过短的到期预警可能使用户无法及时完成凭据轮换；该值只描述新账号默认值。",
			Remediation:         "根据批准的账号策略调整 PASS_WARN_AGE；既有账号必须另行核对。",
			MayAffectConnection: true, SourceRefs: []string{"security-baseline:1.2"},
		},
		directive: "PASS_WARN_AGE", parameterName: "password_warn_days_target",
		defaultTarget: 7, minimumTarget: 1, maximumTarget: 30,
	},
}

func definition(id, name, recommended, directive string) checkutil.Definition {
	return checkutil.Definition{
		ID: id, Name: name, Recommended: recommended, Risk: "medium",
		Description:         "密码质量声明过弱可能降低口令抵抗猜测的能力；单一文件不等于系统最终生效策略。",
		Remediation:         "确认发行版 PAM/pwquality 加载链和项目 Policy 后再通过受控流程调整 " + directive + "。",
		MayAffectConnection: true, SourceRefs: []string{"security-baseline:1.1"},
	}
}

type Plugin struct{}

func New() Plugin { return Plugin{} }

func (Plugin) Metadata() plugin.Metadata {
	checks := make([]plugin.CheckItemDefinition, 0, len(definitions))
	parameters := make([]plugin.Parameter, 0, len(definitions))
	for _, current := range definitions {
		checks = append(checks, plugin.CheckItemDefinition{
			ID: current.ID, Name: current.Name, Description: current.Description,
			RecommendedValue: current.Recommended, SourceRefs: append([]string(nil), current.SourceRefs...),
		})
		parameters = append(parameters, plugin.Parameter{
			Name: current.parameterName, Type: "integer",
			Description: fmt.Sprintf("%s 的批准目标；省略时使用明确标注的 Setpoint 建议", current.directive),
		})
	}
	return plugin.Metadata{
		ID: ID, Category: "Linux", Name: "Linux 密码策略观察", Version: "1.0.0",
		Description: "只读观察 pwquality.conf 声明值和 login.defs 到期警告默认值；不把单一配置文件冒充 PAM 最终生效链。",
		Mode:        plugin.ModeReadOnly, Risk: plugin.RiskLow,
		Impact:           "只读取 /etc/security/pwquality.conf 和 /etc/login.defs",
		SupportedSystems: []string{"linux"}, Parameters: parameters, Checks: checks,
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
		return parameterErrors(selected, parameterErr, now), parameterErr
	}

	var pwqualityValues, loginValues map[string]string
	var pwqualityErr, loginErr error
	if anySelected(selected, true) {
		var contents string
		contents, pwqualityErr = readFixedFile(ctx, input.Executor, pwqualityPath)
		if pwqualityErr == nil {
			pwqualityValues = parseAssignments(contents)
		}
	}
	if anySelected(selected, false) {
		var contents string
		contents, loginErr = readFixedFile(ctx, input.Executor, loginDefsPath)
		if loginErr == nil {
			loginValues = parseLoginDefs(contents)
		}
	}

	items := make([]task.CheckItem, 0, len(definitions))
	var failures []error
	for _, current := range definitions {
		if !checkSelected(selected, current.ID) {
			continue
		}
		observationErr := loginErr
		values := loginValues
		if current.pwquality {
			observationErr = pwqualityErr
			values = pwqualityValues
		}
		if observationErr != nil {
			items = append(items, checkutil.Error(current.Definition,
				checkutil.ErrorCode(observationErr, "password_policy_read_failed"), observationErr.Error(),
				fmt.Sprintf("Unable to read the fixed policy source %s", sourcePath(current)), now))
			failures = append(failures, observationErr)
			continue
		}
		target, err := targetValue(parameters, current)
		if err != nil {
			items = append(items, checkutil.Error(current.Definition, "invalid_check_parameters", err.Error(),
				"The target parameter was rejected before evaluating the observation", now))
			failures = append(failures, err)
			continue
		}
		raw, exists := values[current.directive]
		if !exists {
			items = append(items, checkutil.ManualReview(current.Definition, "not explicitly configured",
				fmt.Sprintf("%s is absent from %s; package defaults or another policy source may apply", current.directive, sourcePath(current)),
				fmt.Sprintf("Parsed only the fixed source %s", sourcePath(current)), now))
			continue
		}
		value, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			parseErr := fmt.Errorf("%s must be an integer, got %q", current.directive, raw)
			items = append(items, checkutil.Error(current.Definition, "password_policy_value_invalid", parseErr.Error(),
				fmt.Sprintf("Parsed %s from %s", current.directive, sourcePath(current)), now))
			failures = append(failures, parseErr)
			continue
		}
		compliant := value >= target
		if current.pwquality && current.directive != "minlen" {
			compliant = value <= target
		}
		evidence := fmt.Sprintf("Parsed %s=%d from %s; observed declaration %s target %d",
			current.directive, value, sourcePath(current), comparisonWord(compliant), target)
		if current.pwquality {
			items = append(items, checkutil.ManualReview(current.Definition, strconv.Itoa(value),
				"pwquality.conf alone does not prove that every effective PAM password stack loads pam_pwquality with this value",
				evidence+"; no PAM include chain was inferred", now))
			continue
		}
		items = append(items, checkutil.Value(current.Definition, strconv.Itoa(value), compliant,
			evidence+"; this is a new-account default and not an existing-account chage result", now))
	}
	return items, errors.Join(failures...)
}

func decodeParameters(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" || string(raw) == "{}" {
		return map[string]json.RawMessage{}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var values map[string]json.RawMessage
	if err := decoder.Decode(&values); err != nil || values == nil {
		return nil, fmt.Errorf("decode Linux password policy parameters: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("decode Linux password policy parameters: trailing JSON value")
	}
	allowed := make(map[string]struct{}, len(definitions))
	for _, current := range definitions {
		allowed[current.parameterName] = struct{}{}
	}
	for name := range values {
		if _, exists := allowed[name]; !exists {
			return nil, fmt.Errorf("unknown Linux password policy parameter %q", name)
		}
	}
	return values, nil
}

func targetValue(parameters map[string]json.RawMessage, current policyDefinition) (int, error) {
	raw, exists := parameters[current.parameterName]
	if !exists {
		return current.defaultTarget, nil
	}
	var value int
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, fmt.Errorf("%s must be an integer", current.parameterName)
	}
	if value < current.minimumTarget || value > current.maximumTarget {
		return 0, fmt.Errorf("%s must be between %d and %d", current.parameterName, current.minimumTarget, current.maximumTarget)
	}
	return value, nil
}

func parameterErrors(selected map[string]struct{}, err error, now time.Time) []task.CheckItem {
	items := make([]task.CheckItem, 0, len(definitions))
	for _, current := range definitions {
		if checkSelected(selected, current.ID) {
			items = append(items, checkutil.Error(current.Definition, "invalid_check_parameters", err.Error(),
				"Linux password policy parameters were rejected before observation", now))
		}
	}
	return items
}

func sourcePath(current policyDefinition) string {
	if current.pwquality {
		return pwqualityPath
	}
	return loginDefsPath
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

func parseAssignments(contents string) map[string]string {
	values := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(contents))
	for scanner.Scan() {
		line := strings.TrimSpace(strings.SplitN(scanner.Text(), "#", 2)[0])
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			parts = []string{fields[0], fields[1]}
		}
		key, value := strings.ToLower(strings.TrimSpace(parts[0])), strings.TrimSpace(parts[1])
		if key != "" && value != "" {
			values[key] = value
		}
	}
	return values
}

func parseLoginDefs(contents string) map[string]string {
	values := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(contents))
	for scanner.Scan() {
		line := strings.TrimSpace(strings.SplitN(scanner.Text(), "#", 2)[0])
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			values[strings.ToUpper(fields[0])] = fields[1]
		}
	}
	return values
}

func comparisonWord(compliant bool) string {
	if compliant {
		return "meets"
	}
	return "does not meet"
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

func anySelected(selected map[string]struct{}, pwquality bool) bool {
	for _, current := range definitions {
		if current.pwquality == pwquality && checkSelected(selected, current.ID) {
			return true
		}
	}
	return false
}
