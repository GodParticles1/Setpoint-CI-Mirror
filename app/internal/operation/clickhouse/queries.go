package clickhouse

import (
	"fmt"
	"sort"
	"strings"
)

const queryVersion = "SELECT version()"

const queryClusters = `SELECT
    cluster,
    toString(shard_num) AS shard_num,
    toString(replica_num) AS replica_num,
    host_name,
    host_address,
    toString(port) AS port,
    toString(is_local) AS is_local
FROM system.clusters
ORDER BY cluster, shard_num, replica_num, host_name`

const queryDisks = `SELECT
    name,
    path,
    toString(free_space) AS free_space,
    toString(total_space) AS total_space,
    toString(keep_free_space) AS keep_free_space
FROM system.disks
ORDER BY name`

func queryTables(database string, tables []string) string {
	return fmt.Sprintf(`SELECT
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
WHERE database = %s AND name IN (%s)
ORDER BY database, name`, quoteLiteral(database), literalList(tables))
}

func queryColumns(database string, tables []string) string {
	return fmt.Sprintf(`SELECT
    database,
    table,
    name,
    toString(position) AS position,
    type,
    default_kind,
    default_expression
FROM system.columns
WHERE database = %s AND table IN (%s)
ORDER BY database, table, position`, quoteLiteral(database), literalList(tables))
}

func queryParts(database string, tables []string) string {
	return fmt.Sprintf(`SELECT
    database,
    table,
    partition,
    toString(sum(rows)) AS rows,
    toString(sum(bytes_on_disk)) AS bytes_on_disk,
    toString(count()) AS active_parts
FROM system.parts
WHERE active = 1 AND database = %s AND table IN (%s)
GROUP BY database, table, partition
ORDER BY database, table, partition`, quoteLiteral(database), literalList(tables))
}

func queryReplicas(database string, tables []string) string {
	return fmt.Sprintf(`SELECT
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
WHERE database = %s AND table IN (%s)
ORDER BY database, table, replica_name`, quoteLiteral(database), literalList(tables))
}

func queryMutations(database string, tables []string) string {
	return fmt.Sprintf(`SELECT
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
WHERE database = %s AND table IN (%s) AND system.mutations.is_done = 0
ORDER BY database, table, create_time, mutation_id`, quoteLiteral(database), literalList(tables))
}

func queryMaterializedViews(database string) string {
	return fmt.Sprintf(`SELECT
    database,
    name,
    create_table_query
FROM system.tables
WHERE database = %s AND engine = 'MaterializedView'
ORDER BY database, name`, quoteLiteral(database))
}

func quoteLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func literalList(values []string) string {
	copyValues := append([]string(nil), values...)
	sort.Strings(copyValues)
	quoted := make([]string, 0, len(copyValues))
	for _, value := range copyValues {
		quoted = append(quoted, quoteLiteral(value))
	}
	return strings.Join(quoted, ",")
}
