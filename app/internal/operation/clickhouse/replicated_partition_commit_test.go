package clickhouse

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type replicaLabState struct {
	mu          sync.Mutex
	fingerprint map[string]DataFingerprint
	parts       map[string]uint64
	source      DataFingerprint
}

func newReplicaLabState(source DataFingerprint) *replicaLabState {
	return &replicaLabState{
		fingerprint: map[string]DataFingerprint{"r1": {}, "r2": {}, "r3": {}},
		parts:       map[string]uint64{"r1": 0, "r2": 0, "r3": 0},
		source:      source,
	}
}

func (state *replicaLabState) set(host string, fingerprint DataFingerprint, parts uint64) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.fingerprint[host] = fingerprint
	state.parts[host] = parts
}

func (state *replicaLabState) setAll(fingerprint DataFingerprint, parts uint64) {
	for _, host := range []string{"r1", "r2", "r3"} {
		state.set(host, fingerprint, parts)
	}
}

type replicaLabVerifier struct{ state *replicaLabState }

func (verifier *replicaLabVerifier) Fingerprint(ctx context.Context, endpoint Endpoint, database string, table Table, _ *TimeRangeFilter) (DataFingerprint, error) {
	return verifier.FingerprintPartition(ctx, endpoint, database, table, "202608")
}

func (verifier *replicaLabVerifier) FingerprintPartition(_ context.Context, endpoint Endpoint, _ string, table Table, _ string) (DataFingerprint, error) {
	verifier.state.mu.Lock()
	defer verifier.state.mu.Unlock()
	if strings.HasPrefix(table.Name, "spmig_") {
		return verifier.state.source, nil
	}
	return verifier.state.fingerprint[endpoint.Host], nil
}

type replicaLabClient struct {
	state       *replicaLabState
	replaceMode string
	dropMode    string
	replaceErr  error
	replaces    int
	drops       int
}

func (client *replicaLabClient) Query(_ context.Context, request QueryRequest) (string, error) {
	switch {
	case strings.Contains(request.Query, "SELECT storage_policy FROM system.tables"):
		return "default", nil
	case strings.Contains(request.Query, "enforce_index_structure_match_on_partition_manipulation"):
		return "0", nil
	case strings.Contains(request.Query, "FROM system.replicas"):
		return fmt.Sprintf(`{"database":"db","table":"events","is_leader":"1","is_readonly":"0","is_session_expired":"0","future_parts":"0","parts_to_check":"0","queue_size":"0","inserts_in_queue":"0","merges_in_queue":"0","log_lag":"0","absolute_delay":"0","zookeeper_path":"/clickhouse/tables/events","replica_name":"%s"}`, request.Host), nil
	case strings.Contains(request.Query, "FROM system.parts"):
		client.state.mu.Lock()
		parts := client.state.parts[request.Host]
		client.state.mu.Unlock()
		return strconv.FormatUint(parts, 10), nil
	case strings.HasPrefix(request.Query, "ALTER TABLE") && strings.Contains(request.Query, " REPLACE PARTITION "):
		client.replaces++
		if client.replaceMode == "partial" {
			client.state.set("r1", client.state.source, 1)
		} else if client.replaceMode != "none" {
			client.state.setAll(client.state.source, 1)
		}
		if client.replaceErr != nil {
			err := client.replaceErr
			client.replaceErr = nil
			return "", err
		}
		return "", nil
	case strings.HasPrefix(request.Query, "ALTER TABLE") && strings.Contains(request.Query, " DROP PARTITION "):
		client.drops++
		if client.dropMode == "partial" {
			client.state.set("r1", DataFingerprint{}, 0)
		} else if client.dropMode == "conflict" {
			client.state.set("r1", DataFingerprint{Rows: 11, HashSum64: "101", HashXor64: "8"}, 1)
		} else {
			client.state.setAll(DataFingerprint{}, 0)
		}
		return "", nil
	default:
		return "", nil
	}
}

func replicaLabSnapshot() Snapshot {
	return Snapshot{
		Role:     RoleTarget,
		Topology: Topology{Mode: "clustered", ClusterNames: []string{"ck"}, Shards: 1, Replicas: 3},
		Clusters: []ClusterMember{
			{Cluster: "ck", ShardNum: 1, ReplicaNum: 1, HostAddress: "r1", Port: 9000, IsLocal: true},
			{Cluster: "ck", ShardNum: 1, ReplicaNum: 2, HostAddress: "r2", Port: 9000},
			{Cluster: "ck", ShardNum: 1, ReplicaNum: 3, HostAddress: "r3", Port: 9000},
		},
	}
}

func replicatedPartitionLabFixture(t *testing.T) (*memoryLedger, *replicaLabState, *replicaLabClient, *ReplicatedPartitionLabCommitEngine, ReplicatedPartitionLabCommitRequest) {
	t.Helper()
	source := DataFingerprint{Rows: 10, HashSum64: "100", HashXor64: "7"}
	state := newReplicaLabState(source)
	client := &replicaLabClient{state: state}
	ledger := newMemoryLedger()
	staging, err := BuildStagingTableName("run-1", "events")
	if err != nil {
		t.Fatal(err)
	}
	entry := LedgerEntry{
		Key:      LedgerKey{RunID: "run-1", Database: "db", Table: "events", Partition: "202608", Chunk: 1},
		Strategy: StrategyNativeStream, State: LedgerVerified, Attempt: 1, StagingTable: staging, Source: source, Target: source, UpdatedAt: time.Now().UTC(),
	}
	if err := ledger.Put(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	engine, err := NewReplicatedPartitionLabCommitEngine(ledger, client, &replicaLabVerifier{state: state}, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	table := replicatedTargetTable()
	table.Name = "events"
	table.Columns = []Column{{Name: "id", Position: 1, Type: "UInt64"}}
	request := ReplicatedPartitionLabCommitRequest{
		Pair:        PairParameters{Source: Endpoint{Host: "source", Port: 9000}, Target: Endpoint{Host: "r1", Port: 9000}, Database: "db", Tables: []string{"events"}},
		Chunk:       TransferChunk{RunID: "run-1", Strategy: StrategyNativeStream, SourceDatabase: "db", SourceTable: "events", TargetDatabase: "db", TargetTable: "events", StagingTable: staging, Partition: "202608", Sequence: 1},
		TargetTable: table, TargetSnapshot: replicaLabSnapshot(),
	}
	return ledger, state, client, engine, request
}

func TestExpectedReplicaTargetsFailClosedForMultipleShards(t *testing.T) {
	snapshot := replicaLabSnapshot()
	snapshot.Clusters[2].ShardNum = 2
	snapshot.Topology.Shards = 2
	if _, err := expectedReplicaTargets(snapshot, Endpoint{}); err == nil {
		t.Fatal("multi-shard target unexpectedly accepted by first replicated partition lab slice")
	}
}

func TestReplicaObserverDistinguishesPendingAndConflict(t *testing.T) {
	_, state, client, _, request := replicatedPartitionLabFixture(t)
	observer, err := NewReplicaPartitionObserver(client, &replicaLabVerifier{state: state})
	if err != nil {
		t.Fatal(err)
	}
	state.setAll(state.source, 1)
	report, err := observer.ObserveSource(context.Background(), request.TargetSnapshot, request.Pair.Target, "db", request.TargetTable, "202608", state.source)
	if err != nil || report.State != ReplicaPartitionConverged || report.Matched != 3 {
		t.Fatalf("converged report=%#v err=%v", report, err)
	}
	state.set("r3", DataFingerprint{}, 0)
	report, err = observer.ObserveSource(context.Background(), request.TargetSnapshot, request.Pair.Target, "db", request.TargetTable, "202608", state.source)
	if err != nil || report.State != ReplicaPartitionPending || report.Absent != 1 {
		t.Fatalf("pending report=%#v err=%v", report, err)
	}
	state.set("r3", DataFingerprint{Rows: 11, HashSum64: "101", HashXor64: "8"}, 1)
	report, err = observer.ObserveSource(context.Background(), request.TargetSnapshot, request.Pair.Target, "db", request.TargetTable, "202608", state.source)
	if err != nil || report.State != ReplicaPartitionConflict || report.Conflicting != 1 {
		t.Fatalf("conflict report=%#v err=%v", report, err)
	}
}

func TestReplicatedPartitionCommitRequiresAllReplicasBeforeCommitted(t *testing.T) {
	_, _, client, engine, request := replicatedPartitionLabFixture(t)
	result, err := engine.Commit(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Entry.State != LedgerCommitted || result.Replicas.Matched != 3 || !result.RollbackAvailable || client.replaces != 1 {
		t.Fatalf("result=%#v replaces=%d", result, client.replaces)
	}
}

func TestReplicatedPartitionCommitRecoversAcceptedStatementAfterClientError(t *testing.T) {
	_, _, client, engine, request := replicatedPartitionLabFixture(t)
	client.replaceErr = errors.New("timeout after server accepted query")
	result, err := engine.Commit(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Entry.State != LedgerCommitted || !result.RecoveredAmbiguous || client.replaces != 1 {
		t.Fatalf("result=%#v replaces=%d", result, client.replaces)
	}
}

func TestReplicatedPartitionCommitPartialPropagationReconcilesWithoutReissue(t *testing.T) {
	_, state, client, engine, request := replicatedPartitionLabFixture(t)
	client.replaceMode = "partial"
	result, err := engine.Commit(context.Background(), request)
	if err == nil || result.Entry.State != LedgerReplicasConverging || client.replaces != 1 {
		t.Fatalf("result=%#v err=%v replaces=%d", result, err, client.replaces)
	}
	state.setAll(state.source, 1)
	result, err = engine.ReconcileCommit(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Entry.State != LedgerCommitted || client.replaces != 1 {
		t.Fatalf("reconcile=%#v replaces=%d", result, client.replaces)
	}
}

func TestReplicatedPartitionCommitUnknownNeverBlindlyReissuesReplace(t *testing.T) {
	ledger, _, client, engine, request := replicatedPartitionLabFixture(t)
	key := LedgerKey{RunID: "run-1", Database: "db", Table: "events", Partition: "202608", Chunk: 1}
	entry, _, _ := ledger.Get(context.Background(), key)
	entry.State, entry.Checkpoint = LedgerCommitUnknown, "commit_unknown"
	if err := ledger.Put(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	result, err := engine.ReconcileCommit(context.Background(), request)
	if err == nil || result.Entry.State != LedgerCommitUnknown || client.replaces != 0 {
		t.Fatalf("result=%#v err=%v replaces=%d", result, err, client.replaces)
	}
}

func TestReplicatedPartitionRollbackBlocksChangedReplica(t *testing.T) {
	ledger, state, client, engine, request := replicatedPartitionLabFixture(t)
	state.setAll(state.source, 1)
	key := LedgerKey{RunID: "run-1", Database: "db", Table: "events", Partition: "202608", Chunk: 1}
	entry, _, _ := ledger.Get(context.Background(), key)
	entry.State = LedgerCommitted
	if err := ledger.Put(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	state.set("r3", DataFingerprint{Rows: 11, HashSum64: "101", HashXor64: "8"}, 1)
	result, err := engine.Rollback(context.Background(), request)
	if err == nil || result.Entry.State != LedgerRollbackBlocked || client.drops != 0 {
		t.Fatalf("result=%#v err=%v drops=%d", result, err, client.drops)
	}
}

func TestReplicatedPartitionRollbackPendingReconcilesWithoutRepeatedDrop(t *testing.T) {
	ledger, state, client, engine, request := replicatedPartitionLabFixture(t)
	state.setAll(state.source, 1)
	key := LedgerKey{RunID: "run-1", Database: "db", Table: "events", Partition: "202608", Chunk: 1}
	entry, _, _ := ledger.Get(context.Background(), key)
	entry.State = LedgerCommitted
	if err := ledger.Put(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	client.dropMode = "partial"
	result, err := engine.Rollback(context.Background(), request)
	if err == nil || result.Entry.State != LedgerRollbackPending || client.drops != 1 {
		t.Fatalf("result=%#v err=%v drops=%d", result, err, client.drops)
	}
	state.setAll(DataFingerprint{}, 0)
	result, err = engine.ReconcileRollback(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Entry.State != LedgerRolledBack || client.drops != 1 || result.Replicas.Absent != 3 {
		t.Fatalf("reconcile=%#v drops=%d", result, client.drops)
	}
}
