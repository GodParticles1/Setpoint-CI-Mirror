package clickhouse

import (
	"bufio"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type tableRow struct {
	Database         string `json:"database"`
	Name             string `json:"name"`
	UUID             string `json:"uuid"`
	Engine           string `json:"engine"`
	EngineFull       string `json:"engine_full"`
	StoragePolicy    string `json:"storage_policy"`
	PartitionKey     string `json:"partition_key"`
	SortingKey       string `json:"sorting_key"`
	PrimaryKey       string `json:"primary_key"`
	CreateTableQuery string `json:"create_table_query"`
}

type columnRow struct {
	Database          string `json:"database"`
	Table             string `json:"table"`
	Name              string `json:"name"`
	Position          string `json:"position"`
	Type              string `json:"type"`
	DefaultKind       string `json:"default_kind"`
	DefaultExpression string `json:"default_expression"`
}

type partRow struct {
	Database    string `json:"database"`
	Table       string `json:"table"`
	Partition   string `json:"partition"`
	Rows        string `json:"rows"`
	BytesOnDisk string `json:"bytes_on_disk"`
	ActiveParts string `json:"active_parts"`
}

type clusterRow struct {
	Cluster     string `json:"cluster"`
	ShardNum    string `json:"shard_num"`
	ReplicaNum  string `json:"replica_num"`
	HostName    string `json:"host_name"`
	HostAddress string `json:"host_address"`
	Port        string `json:"port"`
	IsLocal     string `json:"is_local"`
}

type replicaRow struct {
	Database       string `json:"database"`
	Table          string `json:"table"`
	IsLeader       string `json:"is_leader"`
	IsReadonly     string `json:"is_readonly"`
	SessionExpired string `json:"is_session_expired"`
	FutureParts    string `json:"future_parts"`
	PartsToCheck   string `json:"parts_to_check"`
	QueueSize      string `json:"queue_size"`
	InsertsInQueue string `json:"inserts_in_queue"`
	MergesInQueue  string `json:"merges_in_queue"`
	LogLag         string `json:"log_lag"`
	AbsoluteDelay  string `json:"absolute_delay"`
	ZooKeeperPath  string `json:"zookeeper_path"`
	ReplicaName    string `json:"replica_name"`
}

type mutationRow struct {
	Database         string `json:"database"`
	Table            string `json:"table"`
	MutationID       string `json:"mutation_id"`
	Command          string `json:"command"`
	CreateTime       string `json:"create_time"`
	IsDone           string `json:"is_done"`
	PartsToDo        string `json:"parts_to_do"`
	LatestFailedPart string `json:"latest_failed_part"`
	LatestFailReason string `json:"latest_fail_reason"`
}

type diskRow struct {
	Name          string `json:"name"`
	Path          string `json:"path"`
	FreeSpace     string `json:"free_space"`
	TotalSpace    string `json:"total_space"`
	KeepFreeSpace string `json:"keep_free_space"`
}

type materializedViewRow struct {
	Database         string `json:"database"`
	Name             string `json:"name"`
	CreateTableQuery string `json:"create_table_query"`
}

func decodeJSONEachRow[T any](raw string) ([]T, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	scanner := bufio.NewScanner(strings.NewReader(raw))
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	rows := make([]T, 0)
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		var row T
		if err := json.Unmarshal([]byte(text), &row); err != nil {
			return nil, fmt.Errorf("decode ClickHouse JSONEachRow line %d: %w", line, err)
		}
		rows = append(rows, row)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan ClickHouse JSONEachRow: %w", err)
	}
	return rows, nil
}

func parseTables(raw string) ([]Table, error) {
	rows, err := decodeJSONEachRow[tableRow](raw)
	if err != nil {
		return nil, err
	}
	tables := make([]Table, 0, len(rows))
	for _, row := range rows {
		table := Table{
			Database: row.Database, Name: row.Name, UUID: row.UUID, Engine: row.Engine, EngineFull: row.EngineFull, StoragePolicy: row.StoragePolicy,
			PartitionKey: row.PartitionKey, SortingKey: row.SortingKey, PrimaryKey: row.PrimaryKey,
			CreateTableQuery: row.CreateTableQuery,
		}
		table.IsDistributed = strings.EqualFold(row.Engine, "Distributed")
		table.IsReplicated = strings.HasPrefix(strings.ToLower(row.Engine), "replicated")
		table.IsMaterializedView = strings.EqualFold(row.Engine, "MaterializedView")
		if table.IsDistributed {
			if target, ok := parseDistributedTarget(row.EngineFull); ok {
				table.WriteTarget = &target
			}
		}
		tables = append(tables, table)
	}
	return tables, nil
}

func parseColumns(raw string) (map[string][]Column, error) {
	rows, err := decodeJSONEachRow[columnRow](raw)
	if err != nil {
		return nil, err
	}
	result := make(map[string][]Column)
	for _, row := range rows {
		position, err := parseUint(row.Position)
		if err != nil {
			return nil, err
		}
		key := row.Database + "." + row.Table
		result[key] = append(result[key], Column{Name: row.Name, Position: position, Type: row.Type, DefaultKind: row.DefaultKind, DefaultExpression: row.DefaultExpression})
	}
	for key := range result {
		sort.Slice(result[key], func(i, j int) bool { return result[key][i].Position < result[key][j].Position })
	}
	return result, nil
}

func parseParts(raw string) (map[string][]Partition, error) {
	rows, err := decodeJSONEachRow[partRow](raw)
	if err != nil {
		return nil, err
	}
	result := make(map[string][]Partition)
	for _, row := range rows {
		rowsCount, err := parseUint(row.Rows)
		if err != nil {
			return nil, err
		}
		bytesOnDisk, err := parseUint(row.BytesOnDisk)
		if err != nil {
			return nil, err
		}
		activeParts, err := parseUint(row.ActiveParts)
		if err != nil {
			return nil, err
		}
		key := row.Database + "." + row.Table
		result[key] = append(result[key], Partition{Partition: row.Partition, Rows: rowsCount, BytesOnDisk: bytesOnDisk, ActiveParts: activeParts})
	}
	return result, nil
}

func parseClusters(raw string) ([]ClusterMember, error) {
	rows, err := decodeJSONEachRow[clusterRow](raw)
	if err != nil {
		return nil, err
	}
	result := make([]ClusterMember, 0, len(rows))
	for _, row := range rows {
		shard, err := parseUint(row.ShardNum)
		if err != nil {
			return nil, err
		}
		replica, err := parseUint(row.ReplicaNum)
		if err != nil {
			return nil, err
		}
		port, err := parseUint(row.Port)
		if err != nil {
			return nil, err
		}
		result = append(result, ClusterMember{Cluster: row.Cluster, ShardNum: shard, ReplicaNum: replica, HostName: row.HostName, HostAddress: row.HostAddress, Port: port, IsLocal: parseBool(row.IsLocal)})
	}
	return result, nil
}

func parseReplicas(raw string) ([]Replica, error) {
	rows, err := decodeJSONEachRow[replicaRow](raw)
	if err != nil {
		return nil, err
	}
	result := make([]Replica, 0, len(rows))
	for _, row := range rows {
		future, err := parseUint(row.FutureParts)
		if err != nil {
			return nil, err
		}
		check, err := parseUint(row.PartsToCheck)
		if err != nil {
			return nil, err
		}
		queue, err := parseUint(row.QueueSize)
		if err != nil {
			return nil, err
		}
		inserts, err := parseUint(row.InsertsInQueue)
		if err != nil {
			return nil, err
		}
		merges, err := parseUint(row.MergesInQueue)
		if err != nil {
			return nil, err
		}
		lag, err := parseUint(row.LogLag)
		if err != nil {
			return nil, err
		}
		delay, err := parseUint(row.AbsoluteDelay)
		if err != nil {
			return nil, err
		}
		result = append(result, Replica{Database: row.Database, Table: row.Table, IsLeader: parseBool(row.IsLeader), IsReadonly: parseBool(row.IsReadonly), SessionExpired: parseBool(row.SessionExpired), FutureParts: future, PartsToCheck: check, QueueSize: queue, InsertsInQueue: inserts, MergesInQueue: merges, LogLag: lag, AbsoluteDelay: delay, ZooKeeperPath: row.ZooKeeperPath, ReplicaName: row.ReplicaName})
	}
	return result, nil
}

func parseMutations(raw string) ([]Mutation, error) {
	rows, err := decodeJSONEachRow[mutationRow](raw)
	if err != nil {
		return nil, err
	}
	result := make([]Mutation, 0, len(rows))
	for _, row := range rows {
		parts, err := parseUint(row.PartsToDo)
		if err != nil {
			return nil, err
		}
		result = append(result, Mutation{Database: row.Database, Table: row.Table, MutationID: row.MutationID, Command: row.Command, CreateTime: row.CreateTime, IsDone: parseBool(row.IsDone), PartsToDo: parts, LatestFailedPart: row.LatestFailedPart, LatestFailReason: row.LatestFailReason})
	}
	return result, nil
}

func parseDisks(raw string) ([]Disk, error) {
	rows, err := decodeJSONEachRow[diskRow](raw)
	if err != nil {
		return nil, err
	}
	result := make([]Disk, 0, len(rows))
	for _, row := range rows {
		free, err := parseUint(row.FreeSpace)
		if err != nil {
			return nil, err
		}
		total, err := parseUint(row.TotalSpace)
		if err != nil {
			return nil, err
		}
		keep, err := parseUint(row.KeepFreeSpace)
		if err != nil {
			return nil, err
		}
		result = append(result, Disk{Name: row.Name, Path: row.Path, FreeSpace: free, TotalSpace: total, KeepFreeSpace: keep})
	}
	return result, nil
}

func parseMaterializedViews(raw string) ([]MaterializedView, error) {
	rows, err := decodeJSONEachRow[materializedViewRow](raw)
	if err != nil {
		return nil, err
	}
	result := make([]MaterializedView, 0, len(rows))
	for _, row := range rows {
		view := MaterializedView{Database: row.Database, Name: row.Name}
		fields := strings.Fields(row.CreateTableQuery)
		for i := 0; i+1 < len(fields); i++ {
			if strings.EqualFold(fields[i], "TO") {
				database, table := splitQualifiedIdentifier(fields[i+1], row.Database)
				view.TargetDatabase, view.TargetTable = database, table
				break
			}
		}
		result = append(result, view)
	}
	return result, nil
}

func parseDistributedTarget(engineFull string) (WriteTarget, bool) {
	open := strings.Index(engineFull, "(")
	close := strings.LastIndex(engineFull, ")")
	if open < 0 || close <= open {
		return WriteTarget{}, false
	}
	args := splitArguments(engineFull[open+1 : close])
	if len(args) < 3 {
		return WriteTarget{}, false
	}
	cluster := cleanIdentifierToken(args[0])
	database := cleanIdentifierToken(args[1])
	table := cleanIdentifierToken(args[2])
	if !validIdentifier(database) || !validIdentifier(table) {
		return WriteTarget{}, false
	}
	return WriteTarget{Cluster: cluster, Database: database, Table: table}, true
}

func splitArguments(raw string) []string {
	var result []string
	start := 0
	var quote rune
	for index, character := range raw {
		switch {
		case quote != 0:
			if character == quote {
				quote = 0
			}
		case character == '\'' || character == '"' || character == '`':
			quote = character
		case character == ',':
			result = append(result, strings.TrimSpace(raw[start:index]))
			start = index + 1
		}
	}
	result = append(result, strings.TrimSpace(raw[start:]))
	return result
}

func cleanIdentifierToken(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, "'\"`")
	return value
}

func splitQualifiedIdentifier(value, fallbackDatabase string) (string, string) {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, ";")
	parts := strings.Split(value, ".")
	if len(parts) == 1 {
		return fallbackDatabase, cleanIdentifierToken(parts[0])
	}
	if len(parts) == 2 {
		return cleanIdentifierToken(parts[0]), cleanIdentifierToken(parts[1])
	}
	return "", ""
}
