package linuxnetwork

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"setpoint/internal/executor"
	"setpoint/internal/plugin"
	"setpoint/internal/plugins/checkutil"
	"setpoint/internal/plugins/sysctlconfig"
	"setpoint/internal/task"
)

const ID = "linux.network.source_route"

const hostRoleParameter = "host_role"

var runtimeDefinitions = []checkutil.Definition{
	{
		ID: "net.ipv4.conf.all.accept_source_route", Name: "全局 IPv4 源路由接收",
		Recommended: "批准的非网关主机运行值为 0", Risk: "high",
		Description:         "非网关主机接受源路由可能允许发送方影响数据包路径。",
		Remediation:         "确认主机网络角色后，通过受控变更将运行值和持久化配置设为 0。",
		MayAffectConnection: true, SourceRefs: []string{"security-baseline:1.10"},
	},
	{
		ID: "net.ipv4.conf.default.accept_source_route", Name: "新接口 IPv4 源路由接收默认值",
		Recommended: "批准的非网关主机运行值为 0", Risk: "high",
		Description:         "不安全的 default 值会使后续新增接口继承源路由接收能力。",
		Remediation:         "确认主机网络角色后，通过受控变更将运行值和持久化配置设为 0。",
		MayAffectConnection: true, SourceRefs: []string{"security-baseline:1.10"},
	},
}

var persistedDefinitions = []checkutil.Definition{
	{
		ID: "net.ipv4.conf.all.accept_source_route.persisted", Name: "Persisted global IPv4 source-route acceptance",
		Recommended: "runtime=0; persisted=0", Risk: "high",
		Description:         "A persisted source-route setting can restore an unsafe runtime value during boot or sysctl reload.",
		Remediation:         "After approving the host role, use a controlled change to set both runtime and persistent values to 0.",
		MayAffectConnection: true, SourceRefs: []string{"security-baseline:1.10"},
	},
	{
		ID: "net.ipv4.conf.default.accept_source_route.persisted", Name: "Persisted default IPv4 source-route acceptance",
		Recommended: "runtime=0; persisted=0", Risk: "high",
		Description:         "An unsafe persisted default can be inherited by interfaces created after boot or reload.",
		Remediation:         "After approving the host role, use a controlled change to set both runtime and persistent values to 0.",
		MayAffectConnection: true, SourceRefs: []string{"security-baseline:1.10"},
	},
}

var definitions = append(append([]checkutil.Definition(nil), runtimeDefinitions...), persistedDefinitions...)

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
		ID: ID, Category: "Linux", Name: "Linux IPv4 源路由检查", Version: "1.1.0",
		Description: "只读检查 all/default 的 accept_source_route 运行值与受支持持久化加载视图；主机角色未知时保守要求人工复核。",
		Mode:        plugin.ModeReadOnly, Risk: plugin.RiskLow, Impact: "只读查询固定 sysctl 键及有界持久化配置来源",
		SupportedSystems: []string{"linux"},
		Parameters: []plugin.Parameter{{
			Name: hostRoleParameter, Type: "string", Description: "批准的主机网络角色；默认 unknown",
			Options: []string{"unknown", "non_gateway", "gateway"},
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
	role, err := parseHostRole(input.Parameters)
	if err != nil {
		return selectedErrors(selected, err, now), err
	}
	items := make([]task.CheckItem, 0, len(definitions))
	if role == "gateway" {
		for _, current := range definitions {
			if checkSelected(selected, current.ID) {
				items = append(items, checkutil.NotApplicable(current,
					"The frozen Policy identifies this host as a gateway; the source requirement is scoped to non-gateway hosts", now))
			}
		}
		return items, nil
	}

	var failures []error
	for _, current := range runtimeDefinitions {
		if !checkSelected(selected, current.ID) {
			continue
		}
		result, probeErr := input.Executor.Execute(ctx, executor.Command{Name: "sysctl", Args: []string{"-n", current.ID}})
		if probeErr != nil {
			items = append(items, checkutil.Error(current, checkutil.ErrorCode(probeErr, "source_route_read_failed"),
				probeErr.Error(), "Unable to read the fixed sysctl runtime key", now))
			failures = append(failures, probeErr)
			continue
		}
		if result.StdoutTruncated {
			probeErr = errors.New("sysctl output exceeded the configured limit")
			items = append(items, checkutil.Error(current, "source_route_output_truncated", probeErr.Error(),
				"sysctl output was truncated", now))
			failures = append(failures, probeErr)
			continue
		}
		value := strings.TrimSpace(result.Stdout)
		if value != "0" && value != "1" {
			probeErr = fmt.Errorf("unexpected boolean sysctl value %q", value)
			items = append(items, checkutil.Error(current, "source_route_value_invalid", probeErr.Error(),
				"sysctl returned neither 0 nor 1", now))
			failures = append(failures, probeErr)
			continue
		}
		evidence := fmt.Sprintf("sysctl -n reported %s=%s; no persistent configuration source was inferred", current.ID, value)
		if role == "unknown" {
			items = append(items, checkutil.ManualReview(current, value,
				"The host role is unknown; the source requirement applies only after a non-gateway role is approved",
				evidence, now))
			continue
		}
		items = append(items, checkutil.Value(current, value, value == "0", evidence, now))
	}

	if anySelected(selected, persistedDefinitions) {
		snapshot, snapshotErr := sysctlconfig.Collect(ctx, input.Executor)
		if snapshotErr != nil {
			for _, current := range persistedDefinitions {
				if checkSelected(selected, current.ID) {
					items = append(items, checkutil.Error(current, checkutil.ErrorCode(snapshotErr, "sysctl_persistent_read_failed"),
						snapshotErr.Error(), "Unable to safely collect persistent sysctl sources", now))
				}
			}
			failures = append(failures, snapshotErr)
		} else {
			for _, current := range persistedDefinitions {
				if !checkSelected(selected, current.ID) {
					continue
				}
				key := strings.TrimSuffix(current.ID, ".persisted")
				runtimeValue, runtimeErr := sysctlconfig.ReadRuntimeBoolean(ctx, input.Executor, key)
				if runtimeErr != nil {
					items = append(items, checkutil.Error(current, checkutil.ErrorCode(runtimeErr, "source_route_runtime_read_failed"),
						runtimeErr.Error(), "Unable to read the fixed sysctl runtime key", now))
					failures = append(failures, runtimeErr)
					continue
				}
				resolution := snapshot.Resolve(key)
				items = append(items, persistentSourceRouteItem(current, role, runtimeValue, resolution, now))
			}
		}
	}
	return items, errors.Join(failures...)
}

func persistentSourceRouteItem(definition checkutil.Definition, role, runtimeValue string, resolution sysctlconfig.Resolution, now time.Time) task.CheckItem {
	current := fmt.Sprintf("runtime=%s; persisted=%s", runtimeValue, resolution.Value)
	if resolution.State != sysctlconfig.StateResolved {
		current = fmt.Sprintf("runtime=%s; persisted=%s", runtimeValue, resolution.State)
	}
	evidence := fmt.Sprintf("fixed runtime key read; persistent source_class=%s source_digest=%s", resolution.SourceClass, resolution.Digest)
	if role == "unknown" {
		return checkutil.ManualReview(definition, current,
			"The host role is unknown; the source requirement applies only after a non-gateway role is approved", evidence, now)
	}
	if resolution.State != sysctlconfig.StateResolved {
		return checkutil.ManualReview(definition, current, resolution.Reason, evidence, now)
	}
	return checkutil.Value(definition, current, runtimeValue == "0" && resolution.Value == "0", evidence, now)
}

func anySelected(selected map[string]struct{}, candidates []checkutil.Definition) bool {
	for _, definition := range candidates {
		if checkSelected(selected, definition.ID) {
			return true
		}
	}
	return false
}

func parseHostRole(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" || string(raw) == "{}" {
		return "unknown", nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var values map[string]json.RawMessage
	if err := decoder.Decode(&values); err != nil || values == nil {
		return "", fmt.Errorf("decode Linux source route parameters: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return "", errors.New("decode Linux source route parameters: trailing JSON value")
	}
	for name := range values {
		if name != hostRoleParameter {
			return "", fmt.Errorf("unknown Linux source route parameter %q", name)
		}
	}
	rawRole, exists := values[hostRoleParameter]
	if !exists {
		return "unknown", nil
	}
	var role string
	if err := json.Unmarshal(rawRole, &role); err != nil {
		return "", errors.New("host_role must be a string")
	}
	role = strings.ToLower(strings.TrimSpace(role))
	switch role {
	case "unknown", "non_gateway", "gateway":
		return role, nil
	default:
		return "", fmt.Errorf("unsupported host_role %q", role)
	}
}

func selectedErrors(selected map[string]struct{}, err error, now time.Time) []task.CheckItem {
	items := make([]task.CheckItem, 0, len(definitions))
	for _, current := range definitions {
		if checkSelected(selected, current.ID) {
			items = append(items, checkutil.Error(current, "invalid_check_parameters", err.Error(),
				"Linux source route parameters were rejected before observation", now))
		}
	}
	return items
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
