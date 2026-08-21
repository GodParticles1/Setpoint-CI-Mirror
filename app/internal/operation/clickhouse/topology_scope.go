package clickhouse

import "sort"

// inferTopologyForSelectedTables derives topology only from the tables that
// participate in the requested migration. system.clusters can contain example
// or unrelated cluster definitions even on a standalone ClickHouse server, so
// treating every row as runtime topology produces false cluster detection.
func inferTopologyForSelectedTables(tables []Table, clusters []ClusterMember, replicas []Replica) Topology {
	topology := Topology{Mode: "single", Shards: 1, Replicas: 1}

	routedClusters := make(map[string]struct{})
	hasReplicatedTable := false
	for _, table := range tables {
		if table.IsReplicated {
			hasReplicatedTable = true
		}
		if table.WriteTarget != nil && table.WriteTarget.Cluster != "" {
			routedClusters[table.WriteTarget.Cluster] = struct{}{}
		}
	}

	// A non-replicated selected data set with no Distributed routing remains a
	// standalone topology even if system.clusters exposes test/sample clusters.
	if !hasReplicatedTable && len(replicas) == 0 && len(routedClusters) == 0 {
		return topology
	}

	relevant := routedClusters
	if len(relevant) == 0 {
		relevant = replicaClusterCandidates(clusters)
	}
	if len(relevant) == 0 {
		if hasReplicatedTable || len(replicas) > 0 {
			topology.Mode = "replicated"
		}
		return topology
	}

	byClusterShards := make(map[string]map[uint64]struct{})
	byClusterReplicas := make(map[string]map[uint64]struct{})
	for _, member := range clusters {
		if _, ok := relevant[member.Cluster]; !ok {
			continue
		}
		if byClusterShards[member.Cluster] == nil {
			byClusterShards[member.Cluster] = make(map[uint64]struct{})
		}
		if byClusterReplicas[member.Cluster] == nil {
			byClusterReplicas[member.Cluster] = make(map[uint64]struct{})
		}
		byClusterShards[member.Cluster][member.ShardNum] = struct{}{}
		byClusterReplicas[member.Cluster][member.ReplicaNum] = struct{}{}
		if member.IsLocal {
			topology.LocalMembers++
		}
	}

	for cluster := range relevant {
		topology.ClusterNames = append(topology.ClusterNames, cluster)
		if shards := len(byClusterShards[cluster]); shards > topology.Shards {
			topology.Shards = shards
		}
		if replicasCount := len(byClusterReplicas[cluster]); replicasCount > topology.Replicas {
			topology.Replicas = replicasCount
		}
	}
	sort.Strings(topology.ClusterNames)
	if topology.Shards > 1 || topology.Replicas > 1 || hasReplicatedTable || len(replicas) > 0 || len(routedClusters) > 0 {
		topology.Mode = "clustered"
	}
	return topology
}

func replicaClusterCandidates(clusters []ClusterMember) map[string]struct{} {
	byClusterReplicas := make(map[string]map[uint64]struct{})
	localMembers := make(map[string]int)
	for _, member := range clusters {
		if byClusterReplicas[member.Cluster] == nil {
			byClusterReplicas[member.Cluster] = make(map[uint64]struct{})
		}
		byClusterReplicas[member.Cluster][member.ReplicaNum] = struct{}{}
		if member.IsLocal {
			localMembers[member.Cluster]++
		}
	}
	candidates := make(map[string]struct{})
	for cluster, replicaNumbers := range byClusterReplicas {
		if len(replicaNumbers) > 1 && localMembers[cluster] > 0 {
			candidates[cluster] = struct{}{}
		}
	}
	return candidates
}
