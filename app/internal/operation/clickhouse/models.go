package clickhouse

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const OperationID = "operation.clickhouse.online_migration"

type Role string

const (
	RoleSource Role = "source"
	RoleTarget Role = "target"
)

type Parameters struct {
	Role       Role     `json:"role"`
	Database   string   `json:"database"`
	Tables     []string `json:"tables"`
	Host       string   `json:"host,omitempty"`
	Port       uint16   `json:"port,omitempty"`
	User       string   `json:"user,omitempty"`
	Secure     bool     `json:"secure,omitempty"`
	Profile    string   `json:"profile,omitempty"`
	TimeColumn string   `json:"time_column,omitempty"`
	StartTime  string   `json:"start_time,omitempty"`
	EndTime    string   `json:"end_time,omitempty"`
}

type DiscoveryChecks struct {
	Clusters          bool `json:"clusters"`
	Tables            bool `json:"tables"`
	Columns           bool `json:"columns"`
	Parts             bool `json:"parts"`
	Replicas          bool `json:"replicas"`
	Mutations         bool `json:"mutations"`
	Disks             bool `json:"disks"`
	MaterializedViews bool `json:"materialized_views"`
}

type Snapshot struct {
	SchemaVersion     string             `json:"schema_version"`
	CapturedAt        time.Time          `json:"captured_at"`
	Role              Role               `json:"role"`
	Version           string             `json:"version"`
	Database          string             `json:"database"`
	Requested         []string           `json:"requested_tables"`
	Effective         []string           `json:"effective_tables"`
	Topology          Topology           `json:"topology"`
	Checks            DiscoveryChecks    `json:"checks"`
	Tables            []Table            `json:"tables"`
	Clusters          []ClusterMember    `json:"clusters,omitempty"`
	Replicas          []Replica          `json:"replicas,omitempty"`
	Mutations         []Mutation         `json:"mutations,omitempty"`
	Disks             []Disk             `json:"disks,omitempty"`
	MaterializedViews []MaterializedView `json:"materialized_views,omitempty"`
	Warnings          []string           `json:"warnings,omitempty"`
}

type Topology struct {
	Mode         string   `json:"mode"`
	ClusterNames []string `json:"cluster_names,omitempty"`
	LocalMembers int      `json:"local_members"`
	Shards       int      `json:"shards"`
	Replicas     int      `json:"replicas"`
}

type Table struct {
	Database           string       `json:"database"`
	Name               string       `json:"name"`
	UUID               string       `json:"uuid,omitempty"`
	Engine             string       `json:"engine"`
	EngineFull         string       `json:"engine_full"`
	PartitionKey       string       `json:"partition_key"`
	SortingKey         string       `json:"sorting_key"`
	PrimaryKey         string       `json:"primary_key"`
	StoragePolicy      string       `json:"storage_policy"`
	CreateTableQuery   string       `json:"create_table_query"`
	IsDistributed      bool         `json:"is_distributed"`
	IsReplicated       bool         `json:"is_replicated"`
	IsMaterializedView bool         `json:"is_materialized_view"`
	WriteTarget        *WriteTarget `json:"write_target,omitempty"`
	Columns            []Column     `json:"columns,omitempty"`
	Partitions         []Partition  `json:"partitions,omitempty"`
}

type WriteTarget struct {
	Cluster  string `json:"cluster,omitempty"`
	Database string `json:"database"`
	Table    string `json:"table"`
}

type Column struct {
	Name              string `json:"name"`
	Position          uint64 `json:"position"`
	Type              string `json:"type"`
	DefaultKind       string `json:"default_kind,omitempty"`
	DefaultExpression string `json:"default_expression,omitempty"`
}

type Partition struct {
	Partition   string `json:"partition"`
	Rows        uint64 `json:"rows"`
	BytesOnDisk uint64 `json:"bytes_on_disk"`
	ActiveParts uint64 `json:"active_parts"`
}

type ClusterMember struct {
	Cluster     string `json:"cluster"`
	ShardNum    uint64 `json:"shard_num"`
	ReplicaNum  uint64 `json:"replica_num"`
	HostName    string `json:"host_name"`
	HostAddress string `json:"host_address"`
	Port        uint64 `json:"port"`
	IsLocal     bool   `json:"is_local"`
}

type Replica struct {
	Database       string `json:"database"`
	Table          string `json:"table"`
	IsLeader       bool   `json:"is_leader"`
	IsReadonly     bool   `json:"is_readonly"`
	SessionExpired bool   `json:"is_session_expired"`
	FutureParts    uint64 `json:"future_parts"`
	PartsToCheck   uint64 `json:"parts_to_check"`
	QueueSize      uint64 `json:"queue_size"`
	InsertsInQueue uint64 `json:"inserts_in_queue"`
	MergesInQueue  uint64 `json:"merges_in_queue"`
	LogLag         uint64 `json:"log_lag"`
	AbsoluteDelay  uint64 `json:"absolute_delay"`
	ZooKeeperPath  string `json:"zookeeper_path"`
	ReplicaName    string `json:"replica_name"`
}

type Mutation struct {
	Database         string `json:"database"`
	Table            string `json:"table"`
	MutationID       string `json:"mutation_id"`
	Command          string `json:"command"`
	CreateTime       string `json:"create_time"`
	IsDone           bool   `json:"is_done"`
	PartsToDo        uint64 `json:"parts_to_do"`
	LatestFailedPart string `json:"latest_failed_part,omitempty"`
	LatestFailReason string `json:"latest_fail_reason,omitempty"`
}

type Disk struct {
	Name          string `json:"name"`
	Path          string `json:"path"`
	FreeSpace     uint64 `json:"free_space"`
	TotalSpace    uint64 `json:"total_space"`
	KeepFreeSpace uint64 `json:"keep_free_space"`
}

type MaterializedView struct {
	Database       string `json:"database"`
	Name           string `json:"name"`
	TargetDatabase string `json:"target_database"`
	TargetTable    string `json:"target_table"`
}

type CompatibilityIssue struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Table    string `json:"table,omitempty"`
	Summary  string `json:"summary"`
	Detail   string `json:"detail,omitempty"`
}

type PrecheckReport struct {
	Compatible      bool                 `json:"compatible"`
	Issues          []CompatibilityIssue `json:"issues,omitempty"`
	EstimatedBytes  uint64               `json:"estimated_bytes"`
	TargetFreeBytes uint64               `json:"target_free_bytes"`
}

type AnalysisPlan struct {
	SchemaVersion       string   `json:"schema_version"`
	SafetyApproved      bool     `json:"safety_approved"`
	ImplementationReady bool     `json:"implementation_ready"`
	ApplyEnabled        bool     `json:"apply_enabled"`
	RecommendedStrategy string   `json:"recommended_strategy"`
	CandidateStrategies []string `json:"candidate_strategies,omitempty"`
	Reason              string   `json:"reason"`
	DataTables          []string `json:"data_tables"`
	RoutingTables       []string `json:"routing_tables,omitempty"`
	EstimatedBytes      uint64   `json:"estimated_bytes"`
	RequiresPairPlan    bool     `json:"requires_pair_plan"`
}

var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func ParseParameters(raw json.RawMessage) (Parameters, error) {
	var parameters Parameters
	if len(raw) == 0 {
		return Parameters{}, errors.New("clickhouse migration parameters are required")
	}
	if err := json.Unmarshal(raw, &parameters); err != nil {
		return Parameters{}, fmt.Errorf("decode clickhouse migration parameters: %w", err)
	}
	return normalizeParameters(parameters)
}

func normalizeParameters(parameters Parameters) (Parameters, error) {
	parameters.Database = strings.TrimSpace(parameters.Database)
	parameters.Host = strings.TrimSpace(parameters.Host)
	parameters.User = strings.TrimSpace(parameters.User)
	parameters.Profile = strings.TrimSpace(parameters.Profile)
	parameters.TimeColumn = strings.TrimSpace(parameters.TimeColumn)
	if parameters.Role != RoleSource && parameters.Role != RoleTarget {
		return Parameters{}, fmt.Errorf("unsupported clickhouse role %q", parameters.Role)
	}
	if !validIdentifier(parameters.Database) {
		return Parameters{}, errors.New("clickhouse database must be a simple identifier")
	}
	if len(parameters.Tables) == 0 {
		return Parameters{}, errors.New("at least one clickhouse table is required")
	}
	seen := make(map[string]struct{}, len(parameters.Tables))
	cleaned := make([]string, 0, len(parameters.Tables))
	for _, table := range parameters.Tables {
		table = strings.TrimSpace(table)
		if !validIdentifier(table) {
			return Parameters{}, fmt.Errorf("clickhouse table %q must be a simple identifier", table)
		}
		if _, exists := seen[table]; exists {
			continue
		}
		seen[table] = struct{}{}
		cleaned = append(cleaned, table)
	}
	sort.Strings(cleaned)
	parameters.Tables = cleaned
	if parameters.Host == "" {
		parameters.Host = "127.0.0.1"
	}
	if parameters.Port == 0 {
		if parameters.Secure {
			parameters.Port = 9440
		} else {
			parameters.Port = 9000
		}
	}
	if parameters.TimeColumn != "" && !validIdentifier(parameters.TimeColumn) {
		return Parameters{}, errors.New("time_column must be a simple identifier")
	}
	if (parameters.StartTime == "") != (parameters.EndTime == "") {
		return Parameters{}, errors.New("start_time and end_time must be supplied together")
	}
	return parameters, nil
}

func validIdentifier(value string) bool {
	return identifierPattern.MatchString(value)
}

func parseUint(value string) (uint64, error) {
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse unsigned integer %q: %w", value, err)
	}
	return parsed, nil
}

func parseBool(value string) bool {
	return value == "1" || strings.EqualFold(value, "true")
}
