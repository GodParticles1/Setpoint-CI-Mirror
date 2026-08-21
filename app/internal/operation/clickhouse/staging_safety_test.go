package clickhouse

import (
	"context"
	"strings"
	"testing"
)

type stagingSafetyClient struct { queries []string; engine string }
func (client *stagingSafetyClient) Query(_ context.Context, request QueryRequest) (string, error) { client.queries = append(client.queries, request.Query); if strings.Contains(request.Query, "SELECT engine FROM system.tables") { return client.engine, nil }; return "", nil }

func TestSQLStagingControllerBlocksReplicatedTargetBeforeDDL(t *testing.T) {
	client := &stagingSafetyClient{engine: "ReplicatedMergeTree"}
	controller, err := NewSQLStagingController(client)
	if err != nil { t.Fatal(err) }
	err = controller.Recreate(context.Background(), Endpoint{Host: "target", Port: 9000}, "db", "spmig_events_123", "events")
	if err == nil { t.Fatal("replicated target staging unexpectedly allowed") }
	if len(client.queries) != 1 { t.Fatalf("unexpected DDL after safety check: %v", client.queries) }
}

func TestSQLStagingControllerCreatesSafeMergeTreeStaging(t *testing.T) {
	client := &stagingSafetyClient{engine: "ReplacingMergeTree"}
	controller, err := NewSQLStagingController(client)
	if err != nil { t.Fatal(err) }
	if err := controller.Recreate(context.Background(), Endpoint{Host: "target", Port: 9000}, "db", "spmig_events_123", "events"); err != nil { t.Fatal(err) }
	if len(client.queries) != 3 || !strings.HasPrefix(client.queries[1], "DROP TABLE") || !strings.HasPrefix(client.queries[2], "CREATE TABLE") { t.Fatalf("queries=%v", client.queries) }
}
