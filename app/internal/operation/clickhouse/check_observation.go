package clickhouse

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

const queryPing = "SELECT 1"
const queryReadonlySetting = "SELECT toString(value) FROM system.settings WHERE name = 'readonly'"
const queryHostDatabases = `SELECT name, engine
FROM system.databases
WHERE name NOT IN ('system', 'INFORMATION_SCHEMA', 'information_schema')
ORDER BY name`
const queryHostTables = `SELECT
    database,
    name,
    toString(uuid) AS uuid,
    engine,
    engine_full,
    storage_policy,
    partition_key,
    sorting_key,
    primary_key,
    create_table_query
FROM system.tables
WHERE database NOT IN ('system', 'INFORMATION_SCHEMA', 'information_schema')
ORDER BY database, name`
const queryHostReplicas = `SELECT
    database,
    table,
    toString(is_leader) AS is_leader,
    toString(is_readonly) AS is_readonly,
    toString(is_session_expired) AS is_session_expired,
    toString(future_parts) AS future_parts,
    toString(parts_to_check) AS parts_to_check,
    toString(queue_size) AS queue_size,
    toString(inserts_in_queue) AS inserts_in_queue,
    toString(merges_in_queue) AS merges_in_queue,
    toString(if(log_max_index >= log_pointer, log_max_index - log_pointer, 0)) AS log_lag,
    toString(absolute_delay) AS absolute_delay,
    zookeeper_path,
    replica_name
FROM system.replicas
WHERE database NOT IN ('system', 'INFORMATION_SCHEMA', 'information_schema')
ORDER BY database, table, replica_name`
const queryHostMutations = `SELECT
    database,
    table,
    mutation_id,
    command,
    toString(create_time) AS create_time,
    toString(is_done) AS is_done,
    toString(parts_to_do) AS parts_to_do,
    latest_failed_part,
    latest_fail_reason
FROM system.mutations
WHERE database NOT IN ('system', 'INFORMATION_SCHEMA', 'information_schema')
  AND system.mutations.is_done = 0
ORDER BY database, table, create_time, mutation_id`

type DatabaseObservation struct {
	Name   string `json:"name"`
	Engine string `json:"engine"`
}

type AtomicExchangeObservation struct {
	Database  string `json:"database"`
	Table     string `json:"table"`
	Supported bool   `json:"supported"`
	Reason    string `json:"reason,omitempty"`
	Error     string `json:"error,omitempty"`
}

type HostObservation struct {
	Ping             string                      `json:"ping,omitempty"`
	PingError        string                      `json:"ping_error,omitempty"`
	Version          string                      `json:"version,omitempty"`
	VersionError     string                      `json:"version_error,omitempty"`
	Readonly         string                      `json:"readonly,omitempty"`
	ReadonlyError    string                      `json:"readonly_error,omitempty"`
	Databases        []DatabaseObservation       `json:"databases,omitempty"`
	DatabasesError   string                      `json:"databases_error,omitempty"`
	Tables           []Table                     `json:"tables,omitempty"`
	TablesError      string                      `json:"tables_error,omitempty"`
	Disks            []Disk                      `json:"disks,omitempty"`
	DisksError       string                      `json:"disks_error,omitempty"`
	Clusters         []ClusterMember             `json:"clusters,omitempty"`
	ClustersError    string                      `json:"clusters_error,omitempty"`
	Replicas         []Replica                   `json:"replicas,omitempty"`
	ReplicasError    string                      `json:"replicas_error,omitempty"`
	Mutations        []Mutation                  `json:"mutations,omitempty"`
	MutationsError   string                      `json:"mutations_error,omitempty"`
	AtomicExchange   []AtomicExchangeObservation `json:"atomic_exchange,omitempty"`
	AtomicProbeCount int                         `json:"atomic_probe_count"`
}

type hostDatabaseRow struct {
	Name   string `json:"name"`
	Engine string `json:"engine"`
}

// ObserveLocalHost reuses the migration package's canonical parsers and Atomic
// EXCHANGE capability inspector. Every statement is read-only; the only syntax
// capability probe is EXPLAIN SYNTAX.
func ObserveLocalHost(ctx context.Context, client QueryClient) HostObservation {
	observation := HostObservation{}
	observation.Ping, observation.PingError = queryText(ctx, client, queryPing, FormatTSVRaw)
	observation.Version, observation.VersionError = queryText(ctx, client, queryVersion, FormatTSVRaw)
	observation.Readonly, observation.ReadonlyError = queryText(ctx, client, queryReadonlySetting, FormatTSVRaw)

	if raw, errText := queryText(ctx, client, queryHostDatabases, FormatJSONEachRow); errText != "" {
		observation.DatabasesError = errText
	} else {
		rows, err := decodeJSONEachRow[hostDatabaseRow](raw)
		if err != nil {
			observation.DatabasesError = err.Error()
		} else {
			observation.Databases = make([]DatabaseObservation, 0, len(rows))
			for _, row := range rows {
				observation.Databases = append(observation.Databases, DatabaseObservation{Name: row.Name, Engine: row.Engine})
			}
		}
	}

	if raw, errText := queryText(ctx, client, queryHostTables, FormatJSONEachRow); errText != "" {
		observation.TablesError = errText
	} else if tables, err := parseTables(raw); err != nil {
		observation.TablesError = err.Error()
	} else {
		observation.Tables = tables
	}

	if raw, errText := queryText(ctx, client, queryDisks, FormatJSONEachRow); errText != "" {
		observation.DisksError = errText
	} else if disks, err := parseDisks(raw); err != nil {
		observation.DisksError = err.Error()
	} else {
		observation.Disks = disks
	}

	if raw, errText := queryText(ctx, client, queryClusters, FormatJSONEachRow); errText != "" {
		observation.ClustersError = errText
	} else if clusters, err := parseClusters(raw); err != nil {
		observation.ClustersError = err.Error()
	} else {
		observation.Clusters = clusters
	}

	if raw, errText := queryText(ctx, client, queryHostReplicas, FormatJSONEachRow); errText != "" {
		observation.ReplicasError = errText
	} else if replicas, err := parseReplicas(raw); err != nil {
		observation.ReplicasError = err.Error()
	} else {
		observation.Replicas = replicas
	}

	if raw, errText := queryText(ctx, client, queryHostMutations, FormatJSONEachRow); errText != "" {
		observation.MutationsError = errText
	} else if mutations, err := parseMutations(raw); err != nil {
		observation.MutationsError = err.Error()
	} else {
		observation.Mutations = mutations
	}

	if observation.DatabasesError == "" && observation.TablesError == "" {
		observation.AtomicExchange = inspectHostAtomicExchange(ctx, client, observation.Databases, observation.Tables)
		observation.AtomicProbeCount = len(observation.AtomicExchange)
	}
	return observation
}

func queryText(ctx context.Context, client QueryClient, query string, format QueryFormat) (string, string) {
	if client == nil {
		return "", "ClickHouse query client is required"
	}
	output, err := client.Query(ctx, QueryRequest{Host: "127.0.0.1", Port: 9000, Query: query, Format: format})
	if err != nil {
		return "", err.Error()
	}
	return strings.TrimSpace(output), ""
}

func inspectHostAtomicExchange(ctx context.Context, client QueryClient, databases []DatabaseObservation, tables []Table) []AtomicExchangeObservation {
	engines := make(map[string]string, len(databases))
	for _, database := range databases {
		engines[database.Name] = database.Engine
	}
	candidates := make(map[string]Table)
	for _, table := range tables {
		if engines[table.Database] != "Atomic" || !safeExchangeTableEngine(table.Engine) {
			continue
		}
		current, exists := candidates[table.Database]
		if !exists || table.Name < current.Name {
			candidates[table.Database] = table
		}
	}
	names := make([]string, 0, len(candidates))
	for name := range candidates {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]AtomicExchangeObservation, 0, len(names))
	for _, database := range names {
		table := candidates[database]
		capability, err := InspectCommitCapability(ctx, client, Endpoint{Host: "127.0.0.1", Port: 9000}, database, table.Name)
		item := AtomicExchangeObservation{Database: database, Table: table.Name}
		if err != nil {
			item.Error = fmt.Sprintf("inspect Atomic EXCHANGE capability: %v", err)
		} else {
			item.Supported = capability.ExchangeTables
			item.Reason = capability.Reason
		}
		result = append(result, item)
	}
	return result
}
