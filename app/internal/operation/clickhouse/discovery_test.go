package clickhouse

import (
	"context"
	"strings"
	"testing"
)

type discoveryFakeClient struct{}

func (discoveryFakeClient) Query(_ context.Context, request QueryRequest) (string, error) {
	switch {
	case request.Query == queryVersion:
		return "24.8.1.1", nil
	case strings.Contains(request.Query, "FROM system.clusters"):
		return `{"cluster":"ke","shard_num":"1","replica_num":"1","host_name":"n1","host_address":"10.0.0.1","port":"9000","is_local":"1"}` + "\n" + `{"cluster":"ke","shard_num":"1","replica_num":"2","host_name":"n2","host_address":"10.0.0.2","port":"9000","is_local":"0"}`, nil
	case strings.Contains(request.Query, "FROM system.tables") && strings.Contains(request.Query, "engine = 'MaterializedView'"):
		return "", nil
	case strings.Contains(request.Query, "FROM system.tables"):
		if strings.Contains(request.Query, "alarm_local") {
			return `{"database":"message_center","name":"alarm","engine":"Distributed","engine_full":"Distributed('ke','message_center','alarm_local',rand())","partition_key":"","sorting_key":"","primary_key":"","create_table_query":"CREATE TABLE alarm ENGINE=Distributed('ke','message_center','alarm_local',rand())"}` + "\n" + `{"database":"message_center","name":"alarm_local","engine":"ReplicatedMergeTree","engine_full":"ReplicatedMergeTree('/clickhouse/tables/alarm','{replica}')","partition_key":"toYYYYMM(ts)","sorting_key":"id","primary_key":"id","create_table_query":"CREATE TABLE alarm_local (id UInt64, ts DateTime) ENGINE=ReplicatedMergeTree('/clickhouse/tables/alarm','{replica}') PARTITION BY toYYYYMM(ts) ORDER BY id"}`, nil
		}
		return `{"database":"message_center","name":"alarm","engine":"Distributed","engine_full":"Distributed('ke','message_center','alarm_local',rand())","partition_key":"","sorting_key":"","primary_key":"","create_table_query":"CREATE TABLE alarm ENGINE=Distributed('ke','message_center','alarm_local',rand())"}`, nil
	case strings.Contains(request.Query, "FROM system.columns"):
		return `{"database":"message_center","table":"alarm_local","name":"id","position":"1","type":"UInt64","default_kind":"","default_expression":""}` + "\n" + `{"database":"message_center","table":"alarm_local","name":"ts","position":"2","type":"DateTime","default_kind":"","default_expression":""}`, nil
	case strings.Contains(request.Query, "FROM system.parts"):
		return `{"database":"message_center","table":"alarm_local","partition":"202608","rows":"100","bytes_on_disk":"4096","active_parts":"1"}`, nil
	case strings.Contains(request.Query, "FROM system.replicas"):
		return `{"database":"message_center","table":"alarm_local","is_leader":"1","is_readonly":"0","is_session_expired":"0","future_parts":"0","parts_to_check":"0","queue_size":"0","inserts_in_queue":"0","merges_in_queue":"0","log_lag":"0","absolute_delay":"0","zookeeper_path":"/clickhouse/tables/alarm","replica_name":"r1"}`, nil
	case strings.Contains(request.Query, "FROM system.mutations"):
		return "", nil
	case strings.Contains(request.Query, "FROM system.disks"):
		return `{"name":"default","path":"/var/lib/clickhouse/","free_space":"1000000","total_space":"2000000","keep_free_space":"0"}`, nil
	default:
		return "", nil
	}
}

func TestDiscoveryResolvesDistributedLocalTable(t *testing.T) {
	service, err := NewDiscoveryService(discoveryFakeClient{})
	if err != nil { t.Fatal(err) }
	snapshot, err := service.Discover(context.Background(), Parameters{Role: RoleSource, Database: "message_center", Tables: []string{"alarm"}})
	if err != nil { t.Fatal(err) }
	if snapshot.Version != "24.8.1.1" { t.Fatalf("version=%q", snapshot.Version) }
	if len(snapshot.Effective) != 2 || snapshot.Effective[1] != "alarm_local" { t.Fatalf("effective=%v", snapshot.Effective) }
	logical, ok := findTable(snapshot, "message_center", "alarm")
	if !ok || logical.WriteTarget == nil || logical.WriteTarget.Table != "alarm_local" { t.Fatalf("logical=%#v", logical) }
	physical, ok := resolveDataTable(snapshot, logical)
	if !ok || len(physical.Columns) != 2 || len(physical.Partitions) != 1 { t.Fatalf("physical=%#v", physical) }
	if snapshot.Topology.Shards != 1 || snapshot.Topology.Replicas != 2 { t.Fatalf("topology=%#v", snapshot.Topology) }
}
