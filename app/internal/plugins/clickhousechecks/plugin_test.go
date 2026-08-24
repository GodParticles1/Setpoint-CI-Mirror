package clickhousechecks

import (
	"context"
	"errors"
	"testing"
	"time"

	"setpoint/internal/executor"
	"setpoint/internal/operation/clickhouse"
	"setpoint/internal/plugin"
	"setpoint/internal/plugins/checkutil"
	"setpoint/internal/task"
)

type fakeExecutor struct {
	err error
}

func (candidate fakeExecutor) Execute(context.Context, executor.Command) (executor.Result, error) {
	if candidate.err != nil {
		return executor.Result{}, candidate.err
	}
	return executor.Result{Stdout: "ClickHouse client version 24.8", ExitCode: 0}, nil
}

type noopQueryClient struct{}

func (noopQueryClient) Query(context.Context, clickhouse.QueryRequest) (string, error) {
	return "", nil
}

func TestMetadataIsReadOnlyAndZeroParameter(t *testing.T) {
	metadata := New().Metadata()
	if err := plugin.ValidateMetadata(metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.Mode != plugin.ModeReadOnly {
		t.Fatalf("mode=%q", metadata.Mode)
	}
	if len(metadata.Parameters) != 0 {
		t.Fatalf("parameters=%v; ChecksPage has no parameter input and CK read-only checks must remain executable", metadata.Parameters)
	}
	if len(metadata.Checks) != 13 {
		t.Fatalf("checks=%d want=13", len(metadata.Checks))
	}
}

func TestDetectMissingComponentIsNotApplicable(t *testing.T) {
	missing := executor.ErrCommandNotFound
	detection, err := New().Detect(context.Background(), plugin.CheckInput{Executor: fakeExecutor{err: missing}})
	if err != nil {
		t.Fatal(err)
	}
	if detection.Applicable {
		t.Fatalf("detection=%+v", detection)
	}
}

func TestDetectPermissionFailureIsError(t *testing.T) {
	_, err := New().Detect(context.Background(), plugin.CheckInput{Executor: fakeExecutor{err: errors.New("permission denied")}})
	if err == nil {
		t.Fatal("expected permission failure to remain an error")
	}
}

func TestFiveStateSemantics(t *testing.T) {
	now := time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		id          string
		observation clickhouse.HostObservation
		want        task.ItemStatus
	}{
		{name: "safe endpoint", id: "clickhouse.endpoint.query_reachable", observation: clickhouse.HostObservation{Ping: "1"}, want: task.ItemSafe},
		{name: "query failure", id: "clickhouse.endpoint.query_reachable", observation: clickhouse.HostObservation{PingError: "access denied"}, want: task.ItemError},
		{name: "readonly risk", id: "clickhouse.server.readonly_health", observation: clickhouse.HostObservation{Readonly: "2"}, want: task.ItemUnsafe},
		{name: "unknown readonly", id: "clickhouse.server.readonly_health", observation: clickhouse.HostObservation{Readonly: "future-value"}, want: task.ItemManualReview},
		{name: "replication not applicable", id: "clickhouse.replication.state", observation: clickhouse.HostObservation{Tables: []clickhouse.Table{{Database: "db", Name: "local", Engine: "MergeTree"}}}, want: task.ItemNotApplicable},
		{name: "replica hard blocker", id: "clickhouse.replication.state", observation: clickhouse.HostObservation{Tables: []clickhouse.Table{{Database: "db", Name: "r", Engine: "ReplicatedMergeTree", IsReplicated: true}}, Replicas: []clickhouse.Replica{{Database: "db", Table: "r", IsReadonly: true}}}, want: task.ItemUnsafe},
		{name: "replica backlog review", id: "clickhouse.replication.state", observation: clickhouse.HostObservation{Tables: []clickhouse.Table{{Database: "db", Name: "r", Engine: "ReplicatedMergeTree", IsReplicated: true}}, Replicas: []clickhouse.Replica{{Database: "db", Table: "r", QueueSize: 1}}}, want: task.ItemManualReview},
		{name: "topology review", id: "clickhouse.cluster.topology", observation: clickhouse.HostObservation{Clusters: []clickhouse.ClusterMember{{Cluster: "c", ShardNum: 1, ReplicaNum: 1}}}, want: task.ItemManualReview},
		{name: "mutation blocker", id: "clickhouse.migration.prerequisites", observation: clickhouse.HostObservation{Version: "24.8", Mutations: []clickhouse.Mutation{{Database: "db", Table: "t", MutationID: "m1"}}, Disks: []clickhouse.Disk{{Name: "default", FreeSpace: 10, KeepFreeSpace: 1}}}, want: task.ItemUnsafe},
		{name: "prereq uncertainty review", id: "clickhouse.migration.prerequisites", observation: clickhouse.HostObservation{Version: "24.8", Disks: []clickhouse.Disk{{Name: "default", FreeSpace: 10, KeepFreeSpace: 1}}}, want: task.ItemManualReview},
		{name: "atomic unsupported", id: "clickhouse.atomic_exchange.capability", observation: clickhouse.HostObservation{AtomicExchange: []clickhouse.AtomicExchangeObservation{{Database: "db", Table: "t", Reason: "syntax rejected"}}}, want: task.ItemUnsafe},
		{name: "atomic supported", id: "clickhouse.atomic_exchange.capability", observation: clickhouse.HostObservation{AtomicExchange: []clickhouse.AtomicExchangeObservation{{Database: "db", Table: "t", Supported: true}}}, want: task.ItemSafe},
		{name: "pair never guessed", id: "clickhouse.migration.pair_compatibility", observation: clickhouse.HostObservation{}, want: task.ItemManualReview},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			item := evaluate(definitionByID(t, test.id), "clickhouse-client", test.observation, now)
			if item.Status != test.want {
				t.Fatalf("status=%q want=%q item=%+v", item.Status, test.want, item)
			}
		})
	}
}

func TestCheckSelectionUsesBoundedObservation(t *testing.T) {
	called := 0
	candidate := New()
	candidate.resolve = func(context.Context, executor.CommandExecutor) (clickhouse.QueryClient, string, error) {
		return noopQueryClient{}, "clickhouse-client", nil
	}
	candidate.observe = func(context.Context, clickhouse.QueryClient) clickhouse.HostObservation {
		called++
		return clickhouse.HostObservation{Version: "24.8"}
	}
	candidate.now = func() time.Time { return time.Unix(0, 0).UTC() }
	items, err := candidate.Check(context.Background(), plugin.CheckInput{Executor: fakeExecutor{}, SelectedCheckIDs: []string{"clickhouse.version.detected"}})
	if err != nil {
		t.Fatal(err)
	}
	if called != 1 || len(items) != 1 || items[0].ID != "clickhouse.version.detected" || items[0].Status != task.ItemSafe {
		t.Fatalf("called=%d items=%+v", called, items)
	}
}

func definitionByID(t *testing.T, id string) checkutil.Definition {
	t.Helper()
	for _, definition := range definitions {
		if definition.ID == id {
			return definition
		}
	}
	t.Fatalf("definition %q not found", id)
	return checkutil.Definition{}
}
