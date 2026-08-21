package clickhouse

import "testing"

func TestScopedTopologyIgnoresUnrelatedSystemClustersForStandaloneTables(t *testing.T) {
	tables := []Table{{Database: "message_center", Name: "events", Engine: "MergeTree"}}
	clusters := []ClusterMember{
		{Cluster: "test_cluster_two_shards", ShardNum: 1, ReplicaNum: 1, HostAddress: "127.0.0.1", Port: 9000, IsLocal: true},
		{Cluster: "test_cluster_two_shards", ShardNum: 2, ReplicaNum: 1, HostAddress: "127.0.0.2", Port: 9000},
		{Cluster: "test_unavailable_shard", ShardNum: 1, ReplicaNum: 1, HostAddress: "127.0.0.1", Port: 9000, IsLocal: true},
	}
	topology := inferTopologyForSelectedTables(tables, clusters, nil)
	if topology.Mode != "single" || topology.Shards != 1 || topology.Replicas != 1 || len(topology.ClusterNames) != 0 {
		t.Fatalf("topology=%#v", topology)
	}
}

func TestScopedTopologyUsesDistributedTargetCluster(t *testing.T) {
	tables := []Table{{Database: "metrics", Name: "samples", Engine: "Distributed", IsDistributed: true, WriteTarget: &WriteTarget{Cluster: "default", Database: "metrics", Table: "samples_local"}}}
	clusters := []ClusterMember{
		{Cluster: "default", ShardNum: 1, ReplicaNum: 1, HostAddress: "192.0.2.184", Port: 9000, IsLocal: true},
		{Cluster: "default", ShardNum: 1, ReplicaNum: 2, HostAddress: "192.0.2.185", Port: 9000},
		{Cluster: "default", ShardNum: 1, ReplicaNum: 3, HostAddress: "192.0.2.186", Port: 9000},
		{Cluster: "irrelevant", ShardNum: 1, ReplicaNum: 1, HostAddress: "127.0.0.1", Port: 9000},
		{Cluster: "irrelevant", ShardNum: 2, ReplicaNum: 1, HostAddress: "127.0.0.2", Port: 9000},
	}
	topology := inferTopologyForSelectedTables(tables, clusters, nil)
	if topology.Mode != "clustered" || topology.Shards != 1 || topology.Replicas != 3 || len(topology.ClusterNames) != 1 || topology.ClusterNames[0] != "default" {
		t.Fatalf("topology=%#v", topology)
	}
}
