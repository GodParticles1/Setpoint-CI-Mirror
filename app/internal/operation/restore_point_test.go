package operation

import (
	"encoding/json"
	"testing"
	"time"
)

func TestValidateRestorePoint(t *testing.T) {
	now := time.Now()
	expires := now.Add(time.Hour)
	point := RestorePoint{ID: "rp-1", ProviderID: "clickhouse.partition", OperationID: "operation.clickhouse.online_migration", RunID: "run-1", Status: RestorePointVerified, Targets: []Target{{Kind: TargetDataObject, Component: "clickhouse", Resource: "message_center.alarm"}}, CreatedAt: now.Add(-time.Minute), ExpiresAt: &expires, Manifest: Artifact{SchemaVersion: "v1", Payload: json.RawMessage(`{"partition":"202608"}`)}}
	if err := ValidateRestorePoint(point, now); err != nil {
		t.Fatalf("restore point rejected: %v", err)
	}
	point.Status = RestorePointCreated
	if err := ValidateRestorePoint(point, now); err == nil {
		t.Fatal("unverified restore point accepted")
	}
}
