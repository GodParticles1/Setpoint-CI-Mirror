package clickhousechecks

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"setpoint/internal/executor"
	"setpoint/internal/operation/clickhouse"
	"setpoint/internal/plugin"
	"setpoint/internal/plugins/checkutil"
	"setpoint/internal/task"
)

const ID = "clickhouse.readonly.readiness"

var errComponentAbsent = errors.New("ClickHouse client component is absent")

var definitions = []checkutil.Definition{
	{ID: "clickhouse.component.present", Name: "ClickHouse 客户端组件存在", Recommended: "受信 clickhouse-client 或 clickhouse client 可执行", Risk: "low", Description: "确认节点存在可由 Setpoint Trusted Executable Root 解析的 ClickHouse 客户端入口。", Remediation: "若该节点应运行或管理 ClickHouse，请按产品交付方式安装受支持客户端；不要通过放宽 Trusted Executable Root 绕过检查。", SourceRefs: []string{"operation/clickhouse/client_command"}},
	{ID: "clickhouse.version.detected", Name: "ClickHouse 版本可读取", Recommended: "version() 返回可识别版本", Risk: "medium", Description: "版本是迁移兼容性和能力门禁的基础事实。", Remediation: "修复只读查询权限或客户端连接后重新检查；版本兼容性最终由受控迁移 Precheck 判定。", SourceRefs: []string{"operation/clickhouse/discovery", "operation/clickhouse/version"}},
	{ID: "clickhouse.runtime.available", Name: "ClickHouse Server 运行可达", Recommended: "本机 Native endpoint 只读查询可达", Risk: "high", Description: "Server 不可达时无法形成可靠运行态、目录或迁移前置证据。", Remediation: "确认 ClickHouse Server 进程、Native endpoint、监听和本机访问策略；本检查不启动或修改服务。", SourceRefs: []string{"operation/clickhouse/client"}},
	{ID: "clickhouse.endpoint.query_reachable", Name: "ClickHouse 查询链可达", Recommended: "SELECT 1 成功", Risk: "high", Description: "验证 CommandExecutor 到受信客户端再到本机 ClickHouse endpoint 的只读查询链。", Remediation: "沿 Trusted Executable Root、客户端、endpoint 和查询权限逐层排查；不要改用 shell 或绕过受信执行链。", SourceRefs: []string{"operation/clickhouse/client", "executor/trusted-root"}},
	{ID: "clickhouse.server.readonly_health", Name: "ClickHouse 会话可写能力观察", Recommended: "readonly = 0", Risk: "high", Description: "Controlled Operation Apply 需要可写会话；readonly 非零是明确的写入阻断事实，但本检查本身只读。", Remediation: "由管理员确认账号/会话权限边界；不要在 Check 中提升权限。", SourceRefs: []string{"operation/clickhouse/precheck"}},
	{ID: "clickhouse.disk.capacity_evidence", Name: "ClickHouse 磁盘容量安全证据", Recommended: "每个已发现磁盘 free_space > keep_free_space", Risk: "high", Description: "只验证当前磁盘仍有可用余量；具体迁移所需容量仍必须由 source/target Precheck 按估算数据量计算。", Remediation: "清理或扩容应走独立批准流程；迁移前仍需运行正式 Precheck。", SourceRefs: []string{"operation/clickhouse/discovery", "operation/clickhouse/precheck"}},
	{ID: "clickhouse.catalog.databases", Name: "ClickHouse 数据库目录可发现", Recommended: "system.databases 只读发现成功", Risk: "low", Description: "形成非 system 数据库及其 engine 的可审计事实。", Remediation: "若查询失败，修复 metadata 查询权限；不应通过猜测数据库清单继续迁移。", SourceRefs: []string{"operation/clickhouse/discovery"}},
	{ID: "clickhouse.catalog.tables", Name: "ClickHouse 表目录可发现", Recommended: "system.tables 只读发现成功", Risk: "medium", Description: "形成用户表、engine、replication/distributed 等迁移范围基础事实。", Remediation: "若查询失败，修复 metadata 查询权限；正式迁移必须基于重新发现的表范围。", SourceRefs: []string{"operation/clickhouse/discovery"}},
	{ID: "clickhouse.replication.state", Name: "ClickHouse Replica 状态", Recommended: "适用时 replica 非 readonly/session-expired 且无待确认积压", Risk: "high", Description: "Replica readonly、session expired 是明确阻断；队列、delay、parts_to_check 等积压需要人工判断而不能伪造成安全。", Remediation: "先处理 Replica/ZooKeeper/Keeper 健康和积压，再运行正式 Precheck。", SourceRefs: []string{"operation/clickhouse/discovery", "operation/clickhouse/precheck", "operation/clickhouse/replica_observer"}},
	{ID: "clickhouse.cluster.topology", Name: "ClickHouse Cluster 拓扑事实", Recommended: "拓扑事实已发现并由迁移计划选择明确 scope", Risk: "high", Description: "system.clusters 只能提供拓扑事实；缺少具体迁移 pair/scope 时不能自动宣布拓扑安全。", Remediation: "在 Controlled Operation 中选择 source/target 和表范围，由 Precheck/Plan 做最终拓扑门禁。", SourceRefs: []string{"operation/clickhouse/discovery", "operation/clickhouse/topology_scope"}},
	{ID: "clickhouse.migration.prerequisites", Name: "ClickHouse 迁移前置事实", Recommended: "无本机明确 blocker，并由 source/target Precheck 最终确认", Risk: "critical", Description: "汇总版本、目录、容量、mutation 等本机只读事实；没有 pair 上下文时只能给出 blocker 或人工复核，不能伪造兼容。", Remediation: "使用正式 ClickHouse Controlled Operation 的 Discover/Precheck/Plan 流程完成 pair 级门禁。", SourceRefs: []string{"operation/clickhouse/precheck", "operation/clickhouse/compatibility_profile"}},
	{ID: "clickhouse.atomic_exchange.capability", Name: "Atomic EXCHANGE 能力", Recommended: "适用的 Atomic + non-replicated MergeTree scope 通过 EXPLAIN SYNTAX EXCHANGE TABLES", Risk: "critical", Description: "复用正式迁移的 fail-closed Atomic EXCHANGE capability inspector；只运行 read-only EXPLAIN SYNTAX。", Remediation: "若 capability 不满足，当前安全 Apply 路径必须保持阻断；禁止为启用 Apply 放宽门禁。", SourceRefs: []string{"operation/clickhouse/capability", "operation/clickhouse/operation_definition"}},
	{ID: "clickhouse.migration.pair_compatibility", Name: "ClickHouse Source/Target 兼容性", Recommended: "由显式 source/target pair 的正式 Precheck 判定", Risk: "critical", Description: "普通 Check task 没有 source/target pair 参数，不能把单节点事实伪造成跨版本/跨端兼容结论。", Remediation: "进入 ClickHouse Controlled Operation，使用 SecretRef/request-scoped credential boundary 和正式 pair Precheck。", SourceRefs: []string{"operation/clickhouse/cross_version", "operation/clickhouse/precheck"}},
}

type clientResolver func(context.Context, executor.CommandExecutor) (clickhouse.QueryClient, string, error)
type observer func(context.Context, clickhouse.QueryClient) clickhouse.HostObservation

type Plugin struct {
	resolve clientResolver
	observe observer
	now     func() time.Time
}

func New() Plugin {
	return Plugin{resolve: resolveLocalClient, observe: clickhouse.ObserveLocalHost, now: func() time.Time { return time.Now().UTC() }}
}

func (Plugin) Metadata() plugin.Metadata {
	checks := make([]plugin.CheckItemDefinition, 0, len(definitions))
	for _, definition := range definitions {
		checks = append(checks, plugin.CheckItemDefinition{ID: definition.ID, Name: definition.Name, Description: definition.Description, RecommendedValue: definition.Recommended, SourceRefs: append([]string(nil), definition.SourceRefs...)})
	}
	return plugin.Metadata{
		ID: ID, Category: "ClickHouse", Name: "ClickHouse 只读运行与迁移就绪检查", Version: "1.0.0",
		Description: "通过受信 ClickHouse CLI 收集本机运行、目录、容量、replica、topology 和迁移安全能力事实；不执行整改或写入。",
		Mode: plugin.ModeReadOnly, Risk: plugin.RiskLow, Impact: "只读查询 system 元数据、SELECT 1、version() 与 EXPLAIN SYNTAX；不修改 ClickHouse",
		SupportedSystems: []string{"linux"}, Parameters: []plugin.Parameter{}, Checks: checks,
	}
}

func (candidate Plugin) Detect(ctx context.Context, input plugin.CheckInput) (plugin.Detection, error) {
	if input.Executor == nil {
		return plugin.Detection{}, errors.New("ClickHouse check command executor is required")
	}
	resolve := candidate.resolve
	if resolve == nil {
		resolve = resolveLocalClient
	}
	_, command, err := resolve(ctx, input.Executor)
	if errors.Is(err, errComponentAbsent) {
		return plugin.Detection{Applicable: false, Reason: "trusted ClickHouse client executable is not present"}, nil
	}
	if err != nil {
		return plugin.Detection{}, fmt.Errorf("detect ClickHouse component: %w", err)
	}
	return plugin.Detection{Applicable: true, Reason: "trusted ClickHouse client available via " + command}, nil
}

func (candidate Plugin) Check(ctx context.Context, input plugin.CheckInput) ([]task.CheckItem, error) {
	if input.Executor == nil {
		return nil, errors.New("ClickHouse check command executor is required")
	}
	resolve := candidate.resolve
	if resolve == nil {
		resolve = resolveLocalClient
	}
	client, command, err := resolve(ctx, input.Executor)
	if err != nil {
		return nil, fmt.Errorf("resolve ClickHouse client: %w", err)
	}
	observe := candidate.observe
	if observe == nil {
		observe = clickhouse.ObserveLocalHost
	}
	now := time.Now().UTC()
	if candidate.now != nil {
		now = candidate.now()
	}
	observation := observe(ctx, client)
	selected, err := selectedDefinitions(input.SelectedCheckIDs)
	if err != nil {
		return nil, err
	}
	items := make([]task.CheckItem, 0, len(selected))
	for _, definition := range selected {
		items = append(items, evaluate(definition, command, observation, now))
	}
	return items, nil
}

func resolveLocalClient(ctx context.Context, commandExecutor executor.CommandExecutor) (clickhouse.QueryClient, string, error) {
	candidates := []clickhouse.ClientCommand{clickhouse.DefaultClientCommand(), clickhouse.UnifiedClientCommand("clickhouse")}
	for _, command := range candidates {
		probe, err := command.Build("--version")
		if err != nil {
			return nil, "", err
		}
		if _, err := commandExecutor.Execute(ctx, probe); err != nil {
			if errors.Is(err, executor.ErrCommandNotFound) {
				continue
			}
			return nil, "", err
		}
		client, err := clickhouse.NewExecutorClientWithCommand(commandExecutor, command)
		if err != nil {
			return nil, "", err
		}
		return client, strings.Join(append([]string{command.Name}, command.PrefixArgs...), " "), nil
	}
	return nil, "", errComponentAbsent
}

func selectedDefinitions(ids []string) ([]checkutil.Definition, error) {
	if len(ids) == 0 {
		return append([]checkutil.Definition(nil), definitions...), nil
	}
	byID := make(map[string]checkutil.Definition, len(definitions))
	for _, definition := range definitions {
		byID[definition.ID] = definition
	}
	seen := make(map[string]struct{}, len(ids))
	selected := make([]checkutil.Definition, 0, len(ids))
	for _, id := range ids {
		definition, exists := byID[id]
		if !exists {
			return nil, fmt.Errorf("unknown ClickHouse check %q", id)
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		selected = append(selected, definition)
	}
	return selected, nil
}

func evaluate(definition checkutil.Definition, command string, observation clickhouse.HostObservation, now time.Time) task.CheckItem {
	switch definition.ID {
	case "clickhouse.component.present":
		return checkutil.Value(definition, command, true, "component detection succeeded through CommandExecutor and Trusted Executable Root", now)
	case "clickhouse.version.detected":
		if observation.VersionError != "" {
			return checkutil.Error(definition, "CLICKHOUSE_VERSION_QUERY_FAILED", observation.VersionError, "version() query failed", now)
		}
		if strings.TrimSpace(observation.Version) == "" {
			return checkutil.Error(definition, "CLICKHOUSE_VERSION_EMPTY", "version() returned an empty value", "empty version evidence", now)
		}
		return checkutil.Value(definition, observation.Version, true, "version() returned a non-empty server version", now)
	case "clickhouse.runtime.available":
		if observation.VersionError != "" {
			return checkutil.Error(definition, "CLICKHOUSE_RUNTIME_UNAVAILABLE", observation.VersionError, "server version query failed", now)
		}
		return checkutil.Value(definition, "reachable", true, "version() succeeded against 127.0.0.1:9000", now)
	case "clickhouse.endpoint.query_reachable":
		if observation.PingError != "" {
			return checkutil.Error(definition, "CLICKHOUSE_QUERY_UNREACHABLE", observation.PingError, "SELECT 1 failed", now)
		}
		if strings.TrimSpace(observation.Ping) != "1" {
			return checkutil.ManualReview(definition, observation.Ping, "SELECT 1 returned an unexpected value", "query transport succeeded but response did not equal 1", now)
		}
		return checkutil.Value(definition, "SELECT 1 = 1", true, "read-only query reached the local Native endpoint", now)
	case "clickhouse.server.readonly_health":
		if observation.ReadonlyError != "" {
			return checkutil.Error(definition, "CLICKHOUSE_READONLY_QUERY_FAILED", observation.ReadonlyError, "readonly setting query failed", now)
		}
		value := strings.TrimSpace(observation.Readonly)
		if value == "0" {
			return checkutil.Value(definition, "readonly=0", true, "current session is not constrained by ClickHouse readonly setting", now)
		}
		if value == "1" || value == "2" {
			return checkutil.Value(definition, "readonly="+value, false, "current session is readonly and cannot satisfy a write-capable Controlled Operation", now)
		}
		return checkutil.ManualReview(definition, "readonly="+value, "readonly setting returned an unrecognized value", "cannot safely classify write capability", now)
	case "clickhouse.disk.capacity_evidence":
		return evaluateDisks(definition, observation, now)
	case "clickhouse.catalog.databases":
		if observation.DatabasesError != "" {
			return checkutil.Error(definition, "CLICKHOUSE_DATABASE_DISCOVERY_FAILED", observation.DatabasesError, "system.databases discovery failed", now)
		}
		return checkutil.Value(definition, fmt.Sprintf("%d user database(s)", len(observation.Databases)), true, databaseEvidence(observation.Databases), now)
	case "clickhouse.catalog.tables":
		if observation.TablesError != "" {
			return checkutil.Error(definition, "CLICKHOUSE_TABLE_DISCOVERY_FAILED", observation.TablesError, "system.tables discovery failed", now)
		}
		return checkutil.Value(definition, fmt.Sprintf("%d user table(s)", len(observation.Tables)), true, tableEvidence(observation.Tables), now)
	case "clickhouse.replication.state":
		return evaluateReplicas(definition, observation, now)
	case "clickhouse.cluster.topology":
		return evaluateTopology(definition, observation, now)
	case "clickhouse.migration.prerequisites":
		return evaluateMigrationPrerequisites(definition, observation, now)
	case "clickhouse.atomic_exchange.capability":
		return evaluateAtomicExchange(definition, observation, now)
	case "clickhouse.migration.pair_compatibility":
		return checkutil.ManualReview(definition, "pair context not supplied to read-only Check task", "source/target compatibility requires explicit pair parameters and plan-scoped discovery", "single-node observations cannot prove pair compatibility", now)
	default:
		return checkutil.Error(definition, "CLICKHOUSE_CHECK_UNKNOWN", "unknown ClickHouse check definition", definition.ID, now)
	}
}

func evaluateDisks(definition checkutil.Definition, observation clickhouse.HostObservation, now time.Time) task.CheckItem {
	if observation.DisksError != "" {
		return checkutil.Error(definition, "CLICKHOUSE_DISK_DISCOVERY_FAILED", observation.DisksError, "system.disks discovery failed", now)
	}
	if len(observation.Disks) == 0 {
		return checkutil.ManualReview(definition, "0 disks", "system.disks returned no rows", "capacity cannot be classified", now)
	}
	var total, free, keep uint64
	unsafe := make([]string, 0)
	for _, disk := range observation.Disks {
		total += disk.TotalSpace
		free += disk.FreeSpace
		keep += disk.KeepFreeSpace
		if disk.FreeSpace <= disk.KeepFreeSpace {
			unsafe = append(unsafe, disk.Name)
		}
	}
	current := fmt.Sprintf("free=%d keep_free=%d total=%d", free, keep, total)
	if len(unsafe) > 0 {
		sort.Strings(unsafe)
		return checkutil.Value(definition, current, false, "free_space is at/below keep_free_space on: "+strings.Join(unsafe, ", "), now)
	}
	return checkutil.Value(definition, current, true, "all discovered disks retain positive free space above keep_free_space; migration sizing remains plan-scoped", now)
}

func evaluateReplicas(definition checkutil.Definition, observation clickhouse.HostObservation, now time.Time) task.CheckItem {
	if observation.TablesError != "" {
		return checkutil.Error(definition, "CLICKHOUSE_TABLE_DISCOVERY_FAILED", observation.TablesError, "cannot determine whether replication applies", now)
	}
	replicated := 0
	for _, table := range observation.Tables {
		if table.IsReplicated {
			replicated++
		}
	}
	if replicated == 0 {
		return checkutil.NotApplicable(definition, "no Replicated* user tables were discovered", now)
	}
	if observation.ReplicasError != "" {
		return checkutil.Error(definition, "CLICKHOUSE_REPLICA_DISCOVERY_FAILED", observation.ReplicasError, "system.replicas discovery failed", now)
	}
	if len(observation.Replicas) == 0 {
		return checkutil.Value(definition, "0 replica rows", false, "replicated tables exist but system.replicas returned no matching state", now)
	}
	pending := false
	for _, replica := range observation.Replicas {
		if replica.IsReadonly || replica.SessionExpired {
			return checkutil.Value(definition, replica.Database+"."+replica.Table, false, "replica is readonly or Keeper session is expired", now)
		}
		if replica.QueueSize > 0 || replica.PartsToCheck > 0 || replica.AbsoluteDelay > 0 || replica.LogLag > 0 || replica.FutureParts > 0 {
			pending = true
		}
	}
	if pending {
		return checkutil.ManualReview(definition, fmt.Sprintf("%d replica row(s)", len(observation.Replicas)), "replica backlog/delay requires operator review", "no readonly/session-expired blocker was found, but queue/delay/parts evidence is non-zero", now)
	}
	return checkutil.Value(definition, fmt.Sprintf("%d healthy replica row(s)", len(observation.Replicas)), true, "replicas are not readonly/session-expired and observed queue/delay counters are zero", now)
}

func evaluateTopology(definition checkutil.Definition, observation clickhouse.HostObservation, now time.Time) task.CheckItem {
	if observation.ClustersError != "" {
		return checkutil.Error(definition, "CLICKHOUSE_CLUSTER_DISCOVERY_FAILED", observation.ClustersError, "system.clusters discovery failed", now)
	}
	if len(observation.Clusters) == 0 {
		return checkutil.NotApplicable(definition, "system.clusters returned no configured cluster members", now)
	}
	clusters := make(map[string]struct{})
	shards := make(map[string]struct{})
	for _, member := range observation.Clusters {
		clusters[member.Cluster] = struct{}{}
		shards[member.Cluster+":"+strconv.FormatUint(member.ShardNum, 10)] = struct{}{}
	}
	current := fmt.Sprintf("clusters=%d members=%d shards=%d", len(clusters), len(observation.Clusters), len(shards))
	return checkutil.ManualReview(definition, current, "topology is present but migration scope/pair is not part of this Check task", "topology facts were discovered without declaring them migration-safe", now)
}

func evaluateMigrationPrerequisites(definition checkutil.Definition, observation clickhouse.HostObservation, now time.Time) task.CheckItem {
	for code, errText := range map[string]string{
		"version": observation.VersionError, "databases": observation.DatabasesError, "tables": observation.TablesError,
		"disks": observation.DisksError, "mutations": observation.MutationsError,
	} {
		if errText != "" {
			return checkutil.Error(definition, "CLICKHOUSE_PREREQUISITE_OBSERVATION_FAILED", errText, code+" prerequisite observation failed", now)
		}
	}
	if len(observation.Mutations) > 0 {
		return checkutil.Value(definition, fmt.Sprintf("%d active mutation(s)", len(observation.Mutations)), false, "active system.mutations rows are a known migration blocker", now)
	}
	for _, disk := range observation.Disks {
		if disk.FreeSpace <= disk.KeepFreeSpace {
			return checkutil.Value(definition, "capacity headroom exhausted", false, "at least one disk is at/below keep_free_space", now)
		}
	}
	return checkutil.ManualReview(definition, fmt.Sprintf("version=%s databases=%d tables=%d active_mutations=0", observation.Version, len(observation.Databases), len(observation.Tables)), "no local blocker was observed, but source/target compatibility and migration sizing require the formal pair Precheck", "local evidence is necessary but not sufficient for migration approval", now)
}

func evaluateAtomicExchange(definition checkutil.Definition, observation clickhouse.HostObservation, now time.Time) task.CheckItem {
	if observation.DatabasesError != "" || observation.TablesError != "" {
		errText := strings.TrimSpace(strings.Join(nonEmpty(observation.DatabasesError, observation.TablesError), "; "))
		return checkutil.Error(definition, "CLICKHOUSE_ATOMIC_SCOPE_DISCOVERY_FAILED", errText, "database/table discovery required for capability probe failed", now)
	}
	if len(observation.AtomicExchange) == 0 {
		return checkutil.NotApplicable(definition, "no Atomic database with an eligible non-replicated MergeTree table was discovered", now)
	}
	for _, probe := range observation.AtomicExchange {
		if probe.Error != "" {
			return checkutil.Error(definition, "CLICKHOUSE_ATOMIC_PROBE_FAILED", probe.Error, probe.Database+"."+probe.Table, now)
		}
		if !probe.Supported {
			return checkutil.Value(definition, probe.Database+"."+probe.Table, false, "Atomic EXCHANGE capability rejected: "+probe.Reason, now)
		}
	}
	return checkutil.Value(definition, fmt.Sprintf("%d representative Atomic scope(s) supported", len(observation.AtomicExchange)), true, "each discovered Atomic database with an eligible non-replicated MergeTree sample passed the same EXPLAIN SYNTAX capability inspector used by Controlled Operations", now)
}

func databaseEvidence(databases []clickhouse.DatabaseObservation) string {
	if len(databases) == 0 {
		return "metadata query succeeded; no non-system databases discovered"
	}
	values := make([]string, 0, len(databases))
	for _, database := range databases {
		values = append(values, database.Name+"("+database.Engine+")")
	}
	return "databases: " + strings.Join(values, ", ")
}

func tableEvidence(tables []clickhouse.Table) string {
	if len(tables) == 0 {
		return "metadata query succeeded; no non-system tables discovered"
	}
	var replicated, distributed int
	for _, table := range tables {
		if table.IsReplicated {
			replicated++
		}
		if table.IsDistributed {
			distributed++
		}
	}
	return fmt.Sprintf("tables=%d replicated=%d distributed=%d", len(tables), replicated, distributed)
}

func nonEmpty(values ...string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			result = append(result, value)
		}
	}
	return result
}
