package clickhouse

import (
	"context"
	"strings"
	"testing"
)

func TestTransferWhereClauseUsesMergeTreePartitionID(t *testing.T) {
	chunk := TransferChunk{RunID: "run-1", Strategy: StrategyNativeStream, SourceDatabase: "db", SourceTable: "events", TargetDatabase: "db", TargetTable: "events", StagingTable: "spmig_events_123", Partition: "202608", Sequence: 1}
	where, err := transferWhereClause(chunk)
	if err != nil {
		t.Fatal(err)
	}
	if where != " WHERE _partition_id = '202608'" {
		t.Fatalf("where=%q", where)
	}
}

func TestTransferChunkRejectsPartitionAndTimeFilterTogether(t *testing.T) {
	filter, err := BuildTimeRangeFilter("ts", "2026-08-01T00:00:00Z", "2026-08-02T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	chunk := TransferChunk{RunID: "run-1", Strategy: StrategyNativeStream, SourceDatabase: "db", SourceTable: "events", TargetDatabase: "db", TargetTable: "events", StagingTable: "spmig_events_123", Partition: "202608", Filter: filter, Sequence: 1}
	if err := ValidateTransferChunk(chunk); err == nil {
		t.Fatal("partition plus time filter unexpectedly allowed")
	}
}

type partitionFingerprintClient struct{ query string }

func (client *partitionFingerprintClient) Query(_ context.Context, request QueryRequest) (string, error) {
	client.query = request.Query
	return `{"rows":"2","hash_sum64":"10","hash_xor64":"4"}`, nil
}

func TestQueryFingerprintVerifierScopesByPartitionID(t *testing.T) {
	client := &partitionFingerprintClient{}
	verifier, err := NewQueryFingerprintVerifier(client)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := verifier.FingerprintPartition(context.Background(), Endpoint{Host: "127.0.0.1", Port: 9000}, "db", Table{Name: "events", Columns: []Column{{Name: "id", Type: "UInt64"}}}, "202608")
	if err != nil {
		t.Fatal(err)
	}
	if fingerprint.Rows != 2 || !strings.Contains(client.query, "WHERE _partition_id = '202608'") {
		t.Fatalf("fingerprint=%#v query=%q", fingerprint, client.query)
	}
}

type nonPartitionVerifier struct{}

func (nonPartitionVerifier) Fingerprint(context.Context, Endpoint, string, Table, *TimeRangeFilter) (DataFingerprint, error) {
	return DataFingerprint{}, nil
}

func TestFingerprintChunkRequiresPartitionAwareVerifier(t *testing.T) {
	_, err := fingerprintChunk(context.Background(), nonPartitionVerifier{}, Endpoint{}, "db", Table{Name: "events"}, TransferChunk{Partition: "202608"})
	if err == nil {
		t.Fatal("partition migration unexpectedly accepted a non-partition-aware verifier")
	}
}
