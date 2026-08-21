package clickhouse

import "testing"

func TestParseDistributedTarget(t *testing.T) {
	target, ok := parseDistributedTarget("Distributed('ke_cluster', 'message_center', 'alarm_local', rand())")
	if !ok {
		t.Fatal("distributed target was not parsed")
	}
	if target.Cluster != "ke_cluster" || target.Database != "message_center" || target.Table != "alarm_local" {
		t.Fatalf("unexpected target: %#v", target)
	}
}

func TestParseTablesPreservesCreateQueryJSONEscaping(t *testing.T) {
	raw := `{"database":"message_center","name":"alarm","uuid":"table-uuid","engine":"MergeTree","engine_full":"MergeTree","storage_policy":"cold","partition_key":"toYYYYMM(ts)","sorting_key":"(id)","primary_key":"id","create_table_query":"CREATE TABLE alarm (id UInt64, note String DEFAULT 'a\\nb') ENGINE = MergeTree ORDER BY id"}`
	tables, err := parseTables(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) != 1 || tables[0].Name != "alarm" || tables[0].UUID != "table-uuid" || tables[0].StoragePolicy != "cold" || tables[0].IsDistributed {
		t.Fatalf("unexpected tables: %#v", tables)
	}
}
