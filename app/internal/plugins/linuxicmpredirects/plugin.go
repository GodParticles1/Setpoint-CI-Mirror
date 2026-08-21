package linuxicmpredirects

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"setpoint/internal/executor"
	"setpoint/internal/plugin"
	"setpoint/internal/plugins/checkutil"
	"setpoint/internal/plugins/sysctlconfig"
	"setpoint/internal/task"
)

const ID = "linux.network.icmp_redirects"

var persistedDefinitions = []checkutil.Definition{
	{ID: "net.ipv4.conf.all.accept_redirects.persisted", Name: "Persisted global ICMP redirect acceptance", Recommended: "runtime=0; persisted=0", Risk: "medium", Description: "A persisted redirect setting can restore unsafe acceptance during boot or reload.", Remediation: "Use a controlled change to set both runtime and persistent values to 0.", SourceRefs: []string{"security-baseline:1.13"}},
	{ID: "net.ipv4.conf.default.accept_redirects.persisted", Name: "Persisted default ICMP redirect acceptance", Recommended: "runtime=0; persisted=0", Risk: "medium", Description: "An unsafe persisted default can be inherited by newly created interfaces.", Remediation: "Use a controlled change to set both runtime and persistent values to 0.", SourceRefs: []string{"security-baseline:1.13"}},
	{ID: "net.ipv4.conf.all.send_redirects.persisted", Name: "Persisted global ICMP redirect sending", Recommended: "runtime=0; persisted=0", Risk: "medium", Description: "A persisted setting can restore redirect sending during boot or reload.", Remediation: "Use a controlled change to set both runtime and persistent values to 0.", SourceRefs: []string{"security-baseline:1.13"}},
	{ID: "net.ipv4.conf.default.send_redirects.persisted", Name: "Persisted default ICMP redirect sending", Recommended: "runtime=0; persisted=0", Risk: "medium", Description: "An unsafe persisted default can be inherited by newly created interfaces.", Remediation: "Use a controlled change to set both runtime and persistent values to 0.", SourceRefs: []string{"security-baseline:1.13"}},
}

type Plugin struct{}

func New() Plugin { return Plugin{} }

func (Plugin) Metadata() plugin.Metadata {
	return plugin.Metadata{
		ID: ID, Category: "Linux", Name: "Linux ICMP 重定向", Version: "1.2.0",
		Description: "只读检查四项 IPv4 ICMP Redirect 内核运行值与受支持持久化加载视图，不修改主机配置。",
		Mode:        plugin.ModeReadOnly, Risk: plugin.RiskLow, Impact: "只读查询固定 sysctl 键及有界持久化配置来源",
		SupportedSystems: []string{"linux"}, Parameters: []plugin.Parameter{},
		Checks: []plugin.CheckItemDefinition{
			{ID: "net.ipv4.conf.all.accept_redirects", Name: "全局接收 ICMP Redirect", RecommendedValue: "0", SourceRefs: []string{"security-baseline:1.13"}},
			{ID: "net.ipv4.conf.default.accept_redirects", Name: "新接口接收 ICMP Redirect", RecommendedValue: "0", SourceRefs: []string{"security-baseline:1.13"}},
			{ID: "net.ipv4.conf.all.send_redirects", Name: "全局发送 ICMP Redirect", RecommendedValue: "0", SourceRefs: []string{"security-baseline:1.13"}},
			{ID: "net.ipv4.conf.default.send_redirects", Name: "新接口发送 ICMP Redirect", RecommendedValue: "0", SourceRefs: []string{"security-baseline:1.13"}},
			{ID: "net.ipv4.conf.all.accept_redirects.persisted", Name: "Persisted global ICMP redirect acceptance", RecommendedValue: "runtime=0; persisted=0", SourceRefs: []string{"security-baseline:1.13"}},
			{ID: "net.ipv4.conf.default.accept_redirects.persisted", Name: "Persisted default ICMP redirect acceptance", RecommendedValue: "runtime=0; persisted=0", SourceRefs: []string{"security-baseline:1.13"}},
			{ID: "net.ipv4.conf.all.send_redirects.persisted", Name: "Persisted global ICMP redirect sending", RecommendedValue: "runtime=0; persisted=0", SourceRefs: []string{"security-baseline:1.13"}},
			{ID: "net.ipv4.conf.default.send_redirects.persisted", Name: "Persisted default ICMP redirect sending", RecommendedValue: "runtime=0; persisted=0", SourceRefs: []string{"security-baseline:1.13"}},
		},
	}
}

func (Plugin) Detect(context.Context, plugin.CheckInput) (plugin.Detection, error) {
	return plugin.Detection{Applicable: true}, nil
}

func (Plugin) Check(ctx context.Context, input plugin.CheckInput) ([]task.CheckItem, error) {
	definitions := []definition{
		{key: "net.ipv4.conf.all.accept_redirects", name: "全局接收 ICMP Redirect", risk: "Accepting redirects can allow route manipulation."},
		{key: "net.ipv4.conf.default.accept_redirects", name: "新接口接收 ICMP Redirect", risk: "New interfaces can inherit unsafe redirect acceptance."},
		{key: "net.ipv4.conf.all.send_redirects", name: "全局发送 ICMP Redirect", risk: "Sending redirects is normally unnecessary on non-router hosts."},
		{key: "net.ipv4.conf.default.send_redirects", name: "新接口发送 ICMP Redirect", risk: "New interfaces can inherit redirect sending behavior."},
	}
	items := make([]task.CheckItem, 0, len(definitions)+len(persistedDefinitions))
	var checkErrors []error
	selected := selectedCheckSet(input.SelectedCheckIDs)
	for _, current := range definitions {
		if !checkSelected(selected, current.key) {
			continue
		}
		result, err := input.Executor.Execute(ctx, executor.Command{Name: "sysctl", Args: []string{"-n", current.key}})
		executedAt := time.Now().UTC()
		item := task.CheckItem{
			ID: current.key, Name: current.name, RecommendedValue: "0", Risk: "medium",
			RiskDescription: current.risk, Remediation: "由现场确认后将持久化配置与运行值设为 0；本插件不执行修改。",
			Applicable: true, SupportsAutomaticFix: false, SupportsRollback: false, RequiresRestart: false,
			MayAffectConnection: false, MayAffectBusiness: false, ExecutedAt: executedAt,
		}
		if err != nil {
			item.EvidenceSummary = "Unable to read the sysctl value."
			item.Status = task.ItemError
			item.Error = &task.Failure{Code: errorCode(err), Message: err.Error()}
			checkErrors = append(checkErrors, fmt.Errorf("read %s: %w", current.key, err))
		} else {
			value := strings.TrimSpace(result.Stdout)
			compliant := value == "0"
			item.CurrentValue = value
			item.Compliant = &compliant
			if compliant {
				item.Status = task.ItemSafe
			} else {
				item.Status = task.ItemUnsafe
			}
			item.EvidenceSummary = fmt.Sprintf("sysctl reported %s = %q", current.key, value)
		}
		items = append(items, item)
	}
	if anyPersistedSelected(selected) {
		now := time.Now().UTC()
		snapshot, snapshotErr := sysctlconfig.Collect(ctx, input.Executor)
		if snapshotErr != nil {
			for _, current := range persistedDefinitions {
				if checkSelected(selected, current.ID) {
					items = append(items, checkutil.Error(current, checkutil.ErrorCode(snapshotErr, "sysctl_persistent_read_failed"),
						snapshotErr.Error(), "Unable to safely collect persistent sysctl sources", now))
				}
			}
			checkErrors = append(checkErrors, snapshotErr)
		} else {
			for _, current := range persistedDefinitions {
				if !checkSelected(selected, current.ID) {
					continue
				}
				key := strings.TrimSuffix(current.ID, ".persisted")
				runtimeValue, runtimeErr := sysctlconfig.ReadRuntimeBoolean(ctx, input.Executor, key)
				if runtimeErr != nil {
					items = append(items, checkutil.Error(current, checkutil.ErrorCode(runtimeErr, "redirect_runtime_read_failed"),
						runtimeErr.Error(), "Unable to read the fixed sysctl runtime key", now))
					checkErrors = append(checkErrors, runtimeErr)
					continue
				}
				resolution := snapshot.Resolve(key)
				items = append(items, persistentRedirectItem(current, runtimeValue, resolution, now))
			}
		}
	}
	return items, errors.Join(checkErrors...)
}

func persistentRedirectItem(definition checkutil.Definition, runtimeValue string, resolution sysctlconfig.Resolution, now time.Time) task.CheckItem {
	current := fmt.Sprintf("runtime=%s; persisted=%s", runtimeValue, resolution.Value)
	if resolution.State != sysctlconfig.StateResolved {
		current = fmt.Sprintf("runtime=%s; persisted=%s", runtimeValue, resolution.State)
	}
	evidence := fmt.Sprintf("fixed runtime key read; persistent source_class=%s source_digest=%s", resolution.SourceClass, resolution.Digest)
	if resolution.State != sysctlconfig.StateResolved {
		return checkutil.ManualReview(definition, current, resolution.Reason, evidence, now)
	}
	return checkutil.Value(definition, current, runtimeValue == "0" && resolution.Value == "0", evidence, now)
}

func anyPersistedSelected(selected map[string]struct{}) bool {
	for _, definition := range persistedDefinitions {
		if checkSelected(selected, definition.ID) {
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

type definition struct {
	key  string
	name string
	risk string
}

func errorCode(err error) string {
	var executionError *executor.Error
	if errors.As(err, &executionError) {
		return string(executionError.Kind)
	}
	return "sysctl_read_failed"
}
