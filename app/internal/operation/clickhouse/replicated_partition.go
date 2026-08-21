package clickhouse

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ReplicatedPartitionCapability describes only the next lab candidate for a
// ReplicatedMergeTree target. ReadyForLab never means Apply is enabled.
type ReplicatedPartitionCapability struct {
	TargetEngine           string `json:"target_engine"`
	StoragePolicy          string `json:"storage_policy"`
	EnforceIndexMatch      bool   `json:"enforce_index_structure_match"`
	EnforceIndexMatchKnown bool   `json:"enforce_index_structure_match_known"`
	ReadyForLab            bool   `json:"ready_for_lab"`
	ApplyEnabled           bool   `json:"apply_enabled"`
	Reason                 string `json:"reason,omitempty"`
}

func InspectReplicatedPartitionCapability(ctx context.Context, client QueryClient, endpoint Endpoint, database string, table Table) (ReplicatedPartitionCapability, error) {
	if client == nil { return ReplicatedPartitionCapability{}, errors.New("ClickHouse query client is required") }
	if !validIdentifier(database) || !validIdentifier(table.Name) { return ReplicatedPartitionCapability{}, errors.New("partition capability identifiers are invalid") }
	capability := ReplicatedPartitionCapability{TargetEngine: table.Engine}
	if !table.IsReplicated || !strings.HasSuffix(strings.ToLower(strings.TrimSpace(table.Engine)), "mergetree") {
		capability.Reason = "partition replacement candidate requires a ReplicatedMergeTree-family target"
		return capability, nil
	}
	if strings.TrimSpace(table.PartitionKey) == "" || strings.TrimSpace(table.SortingKey) == "" {
		capability.Reason = "partition replacement candidate requires discovered partition and sorting keys"
		return capability, nil
	}
	storagePolicy, err := client.Query(ctx, queryForEndpoint(endpoint, database,
		fmt.Sprintf("SELECT storage_policy FROM system.tables WHERE database = %s AND name = %s", quoteLiteral(database), quoteLiteral(table.Name)), FormatTSVRaw))
	if err != nil { return capability, fmt.Errorf("discover ClickHouse storage policy: %w", err) }
	storagePolicy = strings.TrimSpace(storagePolicy)
	if storagePolicy == "" {
		capability.Reason = "target storage policy could not be proven"
		return capability, nil
	}
	capability.StoragePolicy = storagePolicy
	enforce, err := client.Query(ctx, queryForEndpoint(endpoint, database,
		"SELECT value FROM system.settings WHERE name = 'enforce_index_structure_match_on_partition_manipulation'", FormatTSVRaw))
	if err != nil { return capability, fmt.Errorf("discover partition structure enforcement setting: %w", err) }
	enforce = strings.TrimSpace(enforce)
	if enforce == "" {
		capability.Reason = "partition structure enforcement setting is absent or unavailable on the target; do not interpret unknown as disabled"
		return capability, nil
	}
	capability.EnforceIndexMatchKnown = true
	capability.EnforceIndexMatch = parseBool(enforce)
	if capability.EnforceIndexMatch {
		capability.Reason = "destination requires exact index/projection parity; current non-replicated staging builder deliberately does not clone those objects"
		return capability, nil
	}
	if !safeDiscoveredExpression(table.PartitionKey) || !safeDiscoveredExpression(table.SortingKey) || (table.PrimaryKey != "" && !safeDiscoveredExpression(table.PrimaryKey)) {
		capability.Reason = "discovered key expression is not safe for generated staging DDL"
		return capability, nil
	}
	capability.ReadyForLab = true
	capability.ApplyEnabled = false
	capability.Reason = "candidate is structurally ready for isolated lab validation; production Apply remains disabled until REPLACE PARTITION, replica convergence and rollback are physically verified"
	return capability, nil
}

func BuildReplicatedPartitionStagingDDL(database, stagingTable, targetTable string, table Table, storagePolicy string) (string, error) {
	if !validIdentifier(database) || !validIdentifier(stagingTable) || !validIdentifier(targetTable) { return "", errors.New("staging DDL identifiers are invalid") }
	if strings.TrimSpace(storagePolicy) == "" { return "", errors.New("storage policy is required") }
	if !safeDiscoveredExpression(table.PartitionKey) || !safeDiscoveredExpression(table.SortingKey) { return "", errors.New("partition and sorting keys must be safe discovered expressions") }
	if table.PrimaryKey != "" && !safeDiscoveredExpression(table.PrimaryKey) { return "", errors.New("primary key must be a safe discovered expression") }

	query := fmt.Sprintf("CREATE TABLE %s.%s AS %s.%s ENGINE = MergeTree PARTITION BY %s ORDER BY %s",
		quoteIdentifier(database), quoteIdentifier(stagingTable), quoteIdentifier(database), quoteIdentifier(targetTable), table.PartitionKey, table.SortingKey)
	if strings.TrimSpace(table.PrimaryKey) != "" && strings.TrimSpace(table.PrimaryKey) != strings.TrimSpace(table.SortingKey) {
		query += " PRIMARY KEY " + table.PrimaryKey
	}
	query += " SETTINGS storage_policy = " + quoteLiteral(storagePolicy)
	return query, nil
}

func BuildReplacePartitionSQL(database, targetTable, stagingTable, partition string) (string, error) {
	if !validIdentifier(database) || !validIdentifier(targetTable) || !validIdentifier(stagingTable) { return "", errors.New("partition replacement identifiers are invalid") }
	partition = strings.TrimSpace(partition)
	if partition == "" { return "", errors.New("partition ID is required") }
	return fmt.Sprintf("ALTER TABLE %s.%s REPLACE PARTITION %s FROM %s.%s",
		quoteIdentifier(database), quoteIdentifier(targetTable), quoteLiteral(partition), quoteIdentifier(database), quoteIdentifier(stagingTable)), nil
}

func safeDiscoveredExpression(expression string) bool {
	expression = strings.TrimSpace(expression)
	if expression == "" { return false }
	for _, marker := range []string{";", "--", "/*", "*/", "\x00"} {
		if strings.Contains(expression, marker) { return false }
	}
	return true
}
