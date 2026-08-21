package clickhouse

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

type Prechecker struct{}

func NewPrechecker() *Prechecker { return &Prechecker{} }

func (checker *Prechecker) Check(source Snapshot, target Snapshot) PrecheckReport {
	report := PrecheckReport{Compatible: true}
	block := func(issue CompatibilityIssue) {
		issue.Severity = "blocking"
		report.Compatible = false
		report.Issues = append(report.Issues, issue)
	}
	warn := func(issue CompatibilityIssue) {
		issue.Severity = "warning"
		report.Issues = append(report.Issues, issue)
	}

	if source.Role != RoleSource || target.Role != RoleTarget {
		block(CompatibilityIssue{Code: "ROLE_MISMATCH", Summary: "source/target discovery roles are invalid"})
	}
	if source.Version == "" || target.Version == "" {
		block(CompatibilityIssue{Code: "VERSION_UNKNOWN", Summary: "ClickHouse version must be discovered on both sides"})
	} else if source.Version != target.Version {
		block(CompatibilityIssue{Code: "VERSION_MISMATCH", Summary: "ClickHouse versions are different", Detail: fmt.Sprintf("source=%s target=%s", source.Version, target.Version)})
	}
	for side, snapshot := range map[string]Snapshot{"source": source, "target": target} {
		if !snapshot.Checks.Tables || !snapshot.Checks.Columns || !snapshot.Checks.Parts {
			block(CompatibilityIssue{Code: "DISCOVERY_INCOMPLETE", Summary: side + " structural discovery is incomplete"})
		}
		if !snapshot.Checks.Mutations {
			block(CompatibilityIssue{Code: "MUTATION_STATUS_UNKNOWN", Summary: side + " mutation status is unavailable"})
		}
	}
	if !sameStrings(source.Requested, target.Requested) {
		block(CompatibilityIssue{Code: "REQUESTED_TABLE_SET_MISMATCH", Summary: "source and target requested table sets are different"})
	}
	if source.Topology.Shards > 1 || target.Topology.Shards > 1 {
		block(CompatibilityIssue{Code: "MULTI_SHARD_UNSUPPORTED_V1", Summary: "first migration version supports only one shard; replication is allowed"})
	}

	for _, logicalName := range source.Requested {
		sourceLogical, sourceOK := findTable(source, source.Database, logicalName)
		targetLogical, targetOK := findTable(target, target.Database, logicalName)
		if !sourceOK {
			block(CompatibilityIssue{Code: "SOURCE_TABLE_MISSING", Table: source.Database + "." + logicalName, Summary: "source table is missing"})
			continue
		}
		if !targetOK {
			block(CompatibilityIssue{Code: "TARGET_TABLE_MISSING", Table: target.Database + "." + logicalName, Summary: "target table is missing"})
			continue
		}
		if sourceLogical.IsMaterializedView || targetLogical.IsMaterializedView {
			block(CompatibilityIssue{Code: "MATERIALIZED_VIEW_AS_DATA_UNSUPPORTED", Table: logicalName, Summary: "materialized views are not migrated as authoritative data in V1"})
			continue
		}
		if sourceLogical.IsDistributed {
			warn(CompatibilityIssue{Code: "SOURCE_DISTRIBUTED_ROUTING", Table: logicalName, Summary: "source Distributed table will be resolved to its local data table"})
		}
		if targetLogical.IsDistributed {
			warn(CompatibilityIssue{Code: "TARGET_DISTRIBUTED_ROUTING", Table: logicalName, Summary: "target Distributed table will be resolved to its local data table"})
		}

		sourceData, sourceDataOK := resolveDataTable(source, sourceLogical)
		targetData, targetDataOK := resolveDataTable(target, targetLogical)
		if !sourceDataOK {
			block(CompatibilityIssue{Code: "SOURCE_DATA_TABLE_UNRESOLVED", Table: logicalName, Summary: "source physical data table cannot be resolved"})
			continue
		}
		if !targetDataOK {
			block(CompatibilityIssue{Code: "TARGET_DATA_TABLE_UNRESOLVED", Table: logicalName, Summary: "target physical data table cannot be resolved"})
			continue
		}
		compareTableStructure(sourceData, targetData, block)

		rows, bytes := tableTotals(sourceData)
		_ = rows
		report.EstimatedBytes += bytes
		targetRows, _ := tableTotals(targetData)
		if targetRows > 0 {
			boundedSchema := true
			if strings.TrimSpace(sourceData.EngineFull) != strings.TrimSpace(targetData.EngineFull) {
				boundedSchema = false
				block(CompatibilityIssue{Code: "NONEMPTY_ENGINE_DEFINITION_MISMATCH", Table: targetData.Database + "." + targetData.Name, Summary: "non-empty restore points require identical source and target engine definitions"})
			}
			if strings.TrimSpace(sourceData.StoragePolicy) != strings.TrimSpace(targetData.StoragePolicy) {
				boundedSchema = false
				block(CompatibilityIssue{Code: "NONEMPTY_STORAGE_POLICY_MISMATCH", Table: targetData.Database + "." + targetData.Name, Summary: "non-empty restore points require identical source and target storage policies"})
			}
			switch {
			case len(source.Requested) != 1 || len(target.Requested) != 1:
				block(CompatibilityIssue{Code: "NONEMPTY_MULTI_TABLE_UNSUPPORTED", Table: targetData.Database + "." + targetData.Name, Summary: "the bounded non-empty restore path supports exactly one requested table", Detail: fmt.Sprintf("source=%d target=%d", len(source.Requested), len(target.Requested))})
			case sourceLogical.IsDistributed || targetLogical.IsDistributed:
				block(CompatibilityIssue{Code: "NONEMPTY_DISTRIBUTED_UNSUPPORTED", Table: targetData.Database + "." + targetData.Name, Summary: "non-empty restore points do not support Distributed routing tables", Detail: fmt.Sprintf("rows=%d", targetRows)})
			case sourceData.IsReplicated || targetData.IsReplicated:
				block(CompatibilityIssue{Code: "NONEMPTY_REPLICATED_UNSUPPORTED", Table: targetData.Database + "." + targetData.Name, Summary: "non-empty restore points do not support replicated source or target tables", Detail: fmt.Sprintf("rows=%d", targetRows)})
			case !singleNodeTopology(source.Topology) || !singleNodeTopology(target.Topology):
				block(CompatibilityIssue{Code: "NONEMPTY_MULTI_NODE_UNSUPPORTED", Table: targetData.Database + "." + targetData.Name, Summary: "non-empty restore points require discovered single-node topology", Detail: fmt.Sprintf("rows=%d source=%s/%d/%d target=%s/%d/%d", targetRows, source.Topology.Mode, source.Topology.Shards, source.Topology.Replicas, target.Topology.Mode, target.Topology.Shards, target.Topology.Replicas)})
			case boundedSchema:
				warn(CompatibilityIssue{Code: "TARGET_NONEMPTY_RESTORE_REQUIRED", Table: targetData.Database + "." + targetData.Name, Summary: "Apply requires a verified run-owned non-empty restore point", Detail: fmt.Sprintf("rows=%d", targetRows)})
			}
		}
		if targetData.IsReplicated {
			if !target.Checks.Replicas {
				block(CompatibilityIssue{Code: "REPLICA_STATUS_UNKNOWN", Table: targetData.Database + "." + targetData.Name, Summary: "target replica status is unavailable"})
			} else {
				checkReplicaHealth(target, targetData, block, warn)
			}
		}
	}

	if len(source.Mutations) > 0 || len(target.Mutations) > 0 {
		for _, mutation := range append(append([]Mutation(nil), source.Mutations...), target.Mutations...) {
			block(CompatibilityIssue{Code: "ACTIVE_MUTATION", Table: mutation.Database + "." + mutation.Table, Summary: "active ClickHouse mutation blocks migration", Detail: mutation.MutationID})
		}
	}

	if !target.Checks.Disks || len(target.Disks) == 0 {
		block(CompatibilityIssue{Code: "TARGET_CAPACITY_UNKNOWN", Summary: "target disk capacity is unavailable"})
	} else {
		for _, disk := range target.Disks {
			usable := disk.FreeSpace
			if disk.KeepFreeSpace < usable {
				usable -= disk.KeepFreeSpace
			} else {
				usable = 0
			}
			report.TargetFreeBytes += usable
		}
		required := report.EstimatedBytes + report.EstimatedBytes/5
		if report.TargetFreeBytes < required {
			block(CompatibilityIssue{Code: "TARGET_CAPACITY_INSUFFICIENT", Summary: "target free space is below the conservative migration requirement", Detail: fmt.Sprintf("required=%d available=%d", required, report.TargetFreeBytes)})
		}
	}

	sort.SliceStable(report.Issues, func(i, j int) bool {
		if report.Issues[i].Severity != report.Issues[j].Severity {
			return report.Issues[i].Severity < report.Issues[j].Severity
		}
		if report.Issues[i].Table != report.Issues[j].Table {
			return report.Issues[i].Table < report.Issues[j].Table
		}
		return report.Issues[i].Code < report.Issues[j].Code
	})
	return report
}

func singleNodeTopology(topology Topology) bool {
	return topology.Mode == "single" && topology.Shards == 1 && topology.Replicas == 1
}

func (checker *Prechecker) CheckContext(ctx context.Context, source Snapshot, target Snapshot) (PrecheckReport, error) {
	if err := ctx.Err(); err != nil {
		return PrecheckReport{}, err
	}
	return checker.Check(source, target), nil
}

func compareTableStructure(source, target Table, block func(CompatibilityIssue)) {
	tableName := source.Database + "." + source.Name
	if engineFamily(source.Engine) != engineFamily(target.Engine) {
		block(CompatibilityIssue{Code: "ENGINE_FAMILY_MISMATCH", Table: tableName, Summary: "source and target table engines are incompatible", Detail: fmt.Sprintf("source=%s target=%s", source.Engine, target.Engine)})
	}
	if normalizeExpression(source.PartitionKey) != normalizeExpression(target.PartitionKey) {
		block(CompatibilityIssue{Code: "PARTITION_KEY_MISMATCH", Table: tableName, Summary: "partition keys are different"})
	}
	if normalizeExpression(source.SortingKey) != normalizeExpression(target.SortingKey) {
		block(CompatibilityIssue{Code: "SORTING_KEY_MISMATCH", Table: tableName, Summary: "sorting keys are different"})
	}
	if normalizeExpression(source.PrimaryKey) != normalizeExpression(target.PrimaryKey) {
		block(CompatibilityIssue{Code: "PRIMARY_KEY_MISMATCH", Table: tableName, Summary: "primary keys are different"})
	}
	if len(source.Columns) != len(target.Columns) {
		block(CompatibilityIssue{Code: "COLUMN_COUNT_MISMATCH", Table: tableName, Summary: "column counts are different", Detail: fmt.Sprintf("source=%d target=%d", len(source.Columns), len(target.Columns))})
		return
	}
	for index := range source.Columns {
		s, t := source.Columns[index], target.Columns[index]
		if s.Name != t.Name || s.Type != t.Type || s.DefaultKind != t.DefaultKind || normalizeExpression(s.DefaultExpression) != normalizeExpression(t.DefaultExpression) {
			block(CompatibilityIssue{Code: "COLUMN_DEFINITION_MISMATCH", Table: tableName, Summary: "column definition is different", Detail: fmt.Sprintf("position=%d source=%s:%s target=%s:%s", index+1, s.Name, s.Type, t.Name, t.Type)})
			return
		}
	}
}

func checkReplicaHealth(snapshot Snapshot, table Table, block func(CompatibilityIssue), warn func(CompatibilityIssue)) {
	matched := false
	for _, replica := range snapshot.Replicas {
		if replica.Database != table.Database || replica.Table != table.Name {
			continue
		}
		matched = true
		if replica.IsReadonly || replica.SessionExpired {
			block(CompatibilityIssue{Code: "TARGET_REPLICA_UNHEALTHY", Table: table.Database + "." + table.Name, Summary: "target replica is read-only or its session is expired", Detail: replica.ReplicaName})
		} else if replica.QueueSize > 0 || replica.AbsoluteDelay > 0 || replica.PartsToCheck > 0 {
			warn(CompatibilityIssue{Code: "TARGET_REPLICA_PENDING_WORK", Table: table.Database + "." + table.Name, Summary: "target replica has pending work", Detail: fmt.Sprintf("queue=%d delay=%d parts_to_check=%d", replica.QueueSize, replica.AbsoluteDelay, replica.PartsToCheck)})
		}
	}
	if !matched {
		block(CompatibilityIssue{Code: "TARGET_REPLICA_ROW_MISSING", Table: table.Database + "." + table.Name, Summary: "Replicated table has no system.replicas row"})
	}
}

func resolveDataTable(snapshot Snapshot, logical Table) (Table, bool) {
	if !logical.IsDistributed {
		return logical, true
	}
	if logical.WriteTarget == nil {
		return Table{}, false
	}
	return findTable(snapshot, logical.WriteTarget.Database, logical.WriteTarget.Table)
}

func findTable(snapshot Snapshot, database, name string) (Table, bool) {
	for _, table := range snapshot.Tables {
		if table.Database == database && table.Name == name {
			return table, true
		}
	}
	return Table{}, false
}

func tableTotals(table Table) (uint64, uint64) {
	var rows, bytes uint64
	for _, partition := range table.Partitions {
		rows += partition.Rows
		bytes += partition.BytesOnDisk
	}
	return rows, bytes
}

func engineFamily(engine string) string {
	engine = strings.TrimSpace(engine)
	if strings.HasPrefix(engine, "Replicated") {
		return strings.TrimPrefix(engine, "Replicated")
	}
	return engine
}

func normalizeExpression(value string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(value, "`", "")), "")
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	l := append([]string(nil), left...)
	r := append([]string(nil), right...)
	sort.Strings(l)
	sort.Strings(r)
	for i := range l {
		if l[i] != r[i] {
			return false
		}
	}
	return true
}
