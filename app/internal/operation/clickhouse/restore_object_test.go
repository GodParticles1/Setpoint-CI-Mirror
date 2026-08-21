package clickhouse

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type restoreObjectQueryClient struct {
	queries []QueryRequest
	query   func(QueryRequest) (string, error)
}

func (client *restoreObjectQueryClient) Query(_ context.Context, request QueryRequest) (string, error) {
	client.queries = append(client.queries, request)
	if client.query == nil {
		return "", nil
	}
	return client.query(request)
}

func TestBuildRestoreTableNameIsStableRunOwnedAndIdentifierSafe(t *testing.T) {
	token := strings.Repeat("01", restoreOwnershipTokenBytes)
	first, err := BuildRestoreTableName("run-restore-1", "events", token)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildRestoreTableName("run-restore-1", "events", token)
	if err != nil {
		t.Fatal(err)
	}
	other, err := BuildRestoreTableName("run-restore-2", "events", token)
	if err != nil {
		t.Fatal(err)
	}
	otherToken, err := BuildRestoreTableName("run-restore-1", "events", strings.Repeat("10", restoreOwnershipTokenBytes))
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first == other || first == otherToken || !validIdentifier(first) || !strings.HasPrefix(first, "sprp_events_") {
		t.Fatalf("first=%q second=%q other=%q other_token=%q", first, second, other, otherToken)
	}
}

func TestRestoreOwnershipTokensAreRandomLowercase128BitHex(t *testing.T) {
	first, err := newRestoreOwnershipToken()
	if err != nil {
		t.Fatal(err)
	}
	second, err := newRestoreOwnershipToken()
	if err != nil {
		t.Fatal(err)
	}
	if first == second || len(first) != 32 || len(second) != 32 || strings.ToLower(first) != first || strings.ToLower(second) != second {
		t.Fatalf("first=%q second=%q", first, second)
	}
	if _, err := BuildRestoreTableName("run", "events", "not-a-token"); err == nil {
		t.Fatal("invalid restore ownership token accepted")
	}
}

func TestTableSchemaFingerprintPreservesEngineQuotedWhitespace(t *testing.T) {
	base := Table{
		Database: "db", Name: "events", Engine: "MergeTree",
		EngineFull: "MergeTree() SETTINGS storage_policy='cold archive'",
		Columns:    []Column{{Name: "id", Position: 1, Type: "UInt64"}},
	}
	left, err := tableSchemaFingerprint(base)
	if err != nil {
		t.Fatal(err)
	}
	base.EngineFull = "MergeTree() SETTINGS storage_policy='coldarchive'"
	right, err := tableSchemaFingerprint(base)
	if err != nil {
		t.Fatal(err)
	}
	if left == right {
		t.Fatal("engine definitions with different quoted whitespace produced the same schema fingerprint")
	}
}

func TestSQLRestoreObjectControllerCreatesBoundedSchemaAndDataCopy(t *testing.T) {
	client := &restoreObjectQueryClient{}
	controller, err := NewSQLRestoreObjectController(client)
	if err != nil {
		t.Fatal(err)
	}
	target := Table{Database: "db", Name: "events", Engine: "MergeTree", Columns: []Column{{Name: "id", Position: 1, Type: "UInt64"}, {Name: "name", Position: 2, Type: "String"}}}
	if err := controller.Create(context.Background(), Endpoint{Host: "target", Port: 9000}, "db", "sprp_events_deadbeef0000", target); err != nil {
		t.Fatal(err)
	}
	if len(client.queries) != 2 {
		t.Fatalf("queries=%#v", client.queries)
	}
	if client.queries[0].Query != "CREATE TABLE `db`.`sprp_events_deadbeef0000` AS `db`.`events`" {
		t.Fatalf("create=%q", client.queries[0].Query)
	}
	if client.queries[1].Query != "INSERT INTO `db`.`sprp_events_deadbeef0000` (`id`,`name`) SELECT `id`,`name` FROM `db`.`events`" {
		t.Fatalf("copy=%q", client.queries[1].Query)
	}
}

func TestSQLRestoreObjectControllerPreservesObjectAfterFailedCopy(t *testing.T) {
	client := &restoreObjectQueryClient{query: func(request QueryRequest) (string, error) {
		if strings.HasPrefix(request.Query, "INSERT INTO") {
			return "", errors.New("injected copy failure")
		}
		return "", nil
	}}
	controller, err := NewSQLRestoreObjectController(client)
	if err != nil {
		t.Fatal(err)
	}
	target := Table{Database: "db", Name: "events", Engine: "MergeTree", Columns: []Column{{Name: "id", Position: 1, Type: "UInt64"}}}
	err = controller.Create(context.Background(), Endpoint{Host: "target", Port: 9000}, "db", "sprp_events_deadbeef0000", target)
	if err == nil || !strings.Contains(err.Error(), "copy target") {
		t.Fatalf("copy failure=%v", err)
	}
	if len(client.queries) != 2 {
		t.Fatalf("queries=%#v", client.queries)
	}
}

func TestSQLRestoreObjectControllerBlocksReplicatedAndInvalidIdentifiers(t *testing.T) {
	client := &restoreObjectQueryClient{}
	controller, err := NewSQLRestoreObjectController(client)
	if err != nil {
		t.Fatal(err)
	}
	target := Table{Database: "db", Name: "events", Engine: "ReplicatedMergeTree", Columns: []Column{{Name: "id", Position: 1, Type: "UInt64"}}}
	if err := controller.Create(context.Background(), Endpoint{}, "db", "sprp_events_deadbeef0000", target); err == nil {
		t.Fatal("replicated restore object unexpectedly allowed")
	}
	if err := controller.Drop(context.Background(), Endpoint{}, "db", "events;DROP"); err == nil {
		t.Fatal("invalid restore cleanup identifier unexpectedly allowed")
	}
	if len(client.queries) != 0 {
		t.Fatalf("unsafe query issued: %#v", client.queries)
	}
}

func TestSQLRestoreObjectControllerInspectsIdentitySchemaAndPartitions(t *testing.T) {
	client := &restoreObjectQueryClient{}
	client.query = func(request QueryRequest) (string, error) {
		switch {
		case strings.Contains(request.Query, "FROM system.tables"):
			return `{"database":"db","name":"events","uuid":"uuid-events","engine":"MergeTree","engine_full":"MergeTree()","partition_key":"","sorting_key":"id","primary_key":"id","create_table_query":"CREATE TABLE db.events"}`, nil
		case strings.Contains(request.Query, "FROM system.columns"):
			return `{"database":"db","table":"events","name":"id","position":"1","type":"UInt64","default_kind":"","default_expression":""}`, nil
		case strings.Contains(request.Query, "FROM system.parts"):
			return `{"database":"db","table":"events","partition":"all","rows":"4","bytes_on_disk":"64","active_parts":"1"}`, nil
		default:
			return "", errors.New("unexpected query")
		}
	}
	controller, err := NewSQLRestoreObjectController(client)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := controller.Inspect(context.Background(), Endpoint{Host: "target", Port: 9000}, "db", "events")
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Exists || snapshot.Identity.UUID != "uuid-events" || snapshot.Identity.SchemaFingerprint == "" || len(snapshot.Partitions) != 1 || snapshot.Partitions[0].Rows != 4 {
		t.Fatalf("snapshot=%#v", snapshot)
	}
}
