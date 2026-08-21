package clickhouse

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

type DiscoveryService struct {
	client QueryClient
}

func NewDiscoveryService(client QueryClient) (*DiscoveryService, error) {
	if client == nil {
		return nil, fmt.Errorf("clickhouse query client is required")
	}
	return &DiscoveryService{client: client}, nil
}

func (service *DiscoveryService) Discover(ctx context.Context, parameters Parameters) (Snapshot, error) {
	parameters, err := normalizeParameters(parameters)
	if err != nil { return Snapshot{}, err }

	snapshot := Snapshot{SchemaVersion: "clickhouse.snapshot.v1", CapturedAt: time.Now().UTC(), Role: parameters.Role, Database: parameters.Database, Requested: append([]string(nil), parameters.Tables...)}

	version, err := service.query(ctx, parameters, queryVersion, FormatTSVRaw)
	if err != nil { return Snapshot{}, fmt.Errorf("discover version: %w", err) }
	snapshot.Version = strings.TrimSpace(version)

	if raw, queryErr := service.query(ctx, parameters, queryClusters, FormatJSONEachRow); queryErr != nil {
		snapshot.Warnings = append(snapshot.Warnings, "clusters: "+queryErr.Error())
	} else if snapshot.Clusters, err = parseClusters(raw); err != nil {
		return Snapshot{}, fmt.Errorf("parse clusters: %w", err)
	} else { snapshot.Checks.Clusters = true }

	rawTables, err := service.query(ctx, parameters, queryTables(parameters.Database, parameters.Tables), FormatJSONEachRow)
	if err != nil { return Snapshot{}, fmt.Errorf("discover tables: %w", err) }
	tables, err := parseTables(rawTables)
	if err != nil { return Snapshot{}, fmt.Errorf("parse tables: %w", err) }
	snapshot.Checks.Tables = true

	effective := append([]string(nil), parameters.Tables...)
	seen := make(map[string]struct{}, len(effective))
	for _, name := range effective { seen[name] = struct{}{} }
	for _, table := range tables {
		if !table.IsDistributed { continue }
		if table.WriteTarget == nil {
			snapshot.Warnings = append(snapshot.Warnings, fmt.Sprintf("distributed target unresolved: %s.%s", table.Database, table.Name))
			continue
		}
		if table.WriteTarget.Database != parameters.Database {
			snapshot.Warnings = append(snapshot.Warnings, fmt.Sprintf("distributed target outside selected database: %s.%s -> %s.%s", table.Database, table.Name, table.WriteTarget.Database, table.WriteTarget.Table))
			continue
		}
		if _, exists := seen[table.WriteTarget.Table]; !exists {
			seen[table.WriteTarget.Table] = struct{}{}
			effective = append(effective, table.WriteTarget.Table)
		}
	}
	sort.Strings(effective)
	snapshot.Effective = effective

	if len(effective) != len(parameters.Tables) {
		rawTables, err = service.query(ctx, parameters, queryTables(parameters.Database, effective), FormatJSONEachRow)
		if err != nil { return Snapshot{}, fmt.Errorf("discover effective tables: %w", err) }
		tables, err = parseTables(rawTables)
		if err != nil { return Snapshot{}, fmt.Errorf("parse effective tables: %w", err) }
	}

	rawColumns, err := service.query(ctx, parameters, queryColumns(parameters.Database, effective), FormatJSONEachRow)
	if err != nil { return Snapshot{}, fmt.Errorf("discover columns: %w", err) }
	columns, err := parseColumns(rawColumns)
	if err != nil { return Snapshot{}, fmt.Errorf("parse columns: %w", err) }
	snapshot.Checks.Columns = true

	rawParts, err := service.query(ctx, parameters, queryParts(parameters.Database, effective), FormatJSONEachRow)
	if err != nil { return Snapshot{}, fmt.Errorf("discover parts: %w", err) }
	parts, err := parseParts(rawParts)
	if err != nil { return Snapshot{}, fmt.Errorf("parse parts: %w", err) }
	snapshot.Checks.Parts = true

	for index := range tables {
		key := tables[index].Database + "." + tables[index].Name
		tables[index].Columns = columns[key]
		tables[index].Partitions = parts[key]
	}
	snapshot.Tables = tables

	if raw, queryErr := service.query(ctx, parameters, queryReplicas(parameters.Database, effective), FormatJSONEachRow); queryErr != nil {
		snapshot.Warnings = append(snapshot.Warnings, "replicas: "+queryErr.Error())
	} else if snapshot.Replicas, err = parseReplicas(raw); err != nil {
		return Snapshot{}, fmt.Errorf("parse replicas: %w", err)
	} else { snapshot.Checks.Replicas = true }

	if raw, queryErr := service.query(ctx, parameters, queryMutations(parameters.Database, effective), FormatJSONEachRow); queryErr != nil {
		snapshot.Warnings = append(snapshot.Warnings, "mutations: "+queryErr.Error())
	} else if snapshot.Mutations, err = parseMutations(raw); err != nil {
		return Snapshot{}, fmt.Errorf("parse mutations: %w", err)
	} else { snapshot.Checks.Mutations = true }

	if raw, queryErr := service.query(ctx, parameters, queryDisks, FormatJSONEachRow); queryErr != nil {
		snapshot.Warnings = append(snapshot.Warnings, "disks: "+queryErr.Error())
	} else if snapshot.Disks, err = parseDisks(raw); err != nil {
		return Snapshot{}, fmt.Errorf("parse disks: %w", err)
	} else { snapshot.Checks.Disks = true }

	if raw, queryErr := service.query(ctx, parameters, queryMaterializedViews(parameters.Database), FormatJSONEachRow); queryErr != nil {
		snapshot.Warnings = append(snapshot.Warnings, "materialized_views: "+queryErr.Error())
	} else if snapshot.MaterializedViews, err = parseMaterializedViews(raw); err != nil {
		return Snapshot{}, fmt.Errorf("parse materialized views: %w", err)
	} else { snapshot.Checks.MaterializedViews = true }

	snapshot.Topology = inferTopologyForSelectedTables(snapshot.Tables, snapshot.Clusters, snapshot.Replicas)
	return snapshot, nil
}

func (service *DiscoveryService) query(ctx context.Context, parameters Parameters, query string, format QueryFormat) (string, error) {
	return service.client.Query(ctx, QueryRequest{Host: parameters.Host, Port: parameters.Port, User: parameters.User, Secure: parameters.Secure, Database: parameters.Database, Query: query, Format: format})
}

func inferTopology(clusters []ClusterMember, replicas []Replica) Topology {
	topology := Topology{Mode: "single", Shards: 1, Replicas: 1}
	if len(clusters) == 0 {
		if len(replicas) > 0 { topology.Mode = "replicated" }
		return topology
	}
	clusterNames := map[string]struct{}{}
	maxShards, maxReplicas := 1, 1
	byClusterShards := map[string]map[uint64]struct{}{}
	byClusterReplicas := map[string]map[uint64]struct{}{}
	for _, member := range clusters {
		clusterNames[member.Cluster] = struct{}{}
		if member.IsLocal { topology.LocalMembers++ }
		if byClusterShards[member.Cluster] == nil { byClusterShards[member.Cluster] = map[uint64]struct{}{} }
		if byClusterReplicas[member.Cluster] == nil { byClusterReplicas[member.Cluster] = map[uint64]struct{}{} }
		byClusterShards[member.Cluster][member.ShardNum] = struct{}{}
		byClusterReplicas[member.Cluster][member.ReplicaNum] = struct{}{}
	}
	for cluster := range clusterNames {
		topology.ClusterNames = append(topology.ClusterNames, cluster)
		if len(byClusterShards[cluster]) > maxShards { maxShards = len(byClusterShards[cluster]) }
		if len(byClusterReplicas[cluster]) > maxReplicas { maxReplicas = len(byClusterReplicas[cluster]) }
	}
	sort.Strings(topology.ClusterNames)
	topology.Shards, topology.Replicas = maxShards, maxReplicas
	if maxShards > 1 || maxReplicas > 1 || len(replicas) > 0 { topology.Mode = "clustered" }
	return topology
}

func encodeSnapshot(snapshot Snapshot) ([]byte, error) { return json.Marshal(snapshot) }