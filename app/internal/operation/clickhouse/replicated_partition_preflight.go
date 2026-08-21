package clickhouse

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

type ReplicatedPartitionLabPreflight struct {
	Passed            bool                          `json:"passed"`
	Database          string                        `json:"database"`
	Partition         string                        `json:"partition"`
	SourceTable       string                        `json:"source_table"`
	TargetTable       string                        `json:"target_table"`
	SourceFingerprint DataFingerprint               `json:"source_fingerprint"`
	SourceBytes       uint64                        `json:"source_bytes"`
	Capability        ReplicatedPartitionCapability `json:"capability"`
	TargetReplicas    ReplicaPartitionReport        `json:"target_replicas"`
	Findings          []string                      `json:"findings,omitempty"`
}

type ReplicatedPartitionLabPreflightService struct {
	client    QueryClient
	partition PartitionFingerprintVerifier
	observer  *ReplicaPartitionObserver
}

func NewReplicatedPartitionLabPreflightService(client QueryClient, verifier FingerprintVerifier) (*ReplicatedPartitionLabPreflightService, error) {
	if client == nil || verifier == nil {
		return nil, errors.New("ClickHouse query client and fingerprint verifier are required")
	}
	partition, ok := verifier.(PartitionFingerprintVerifier)
	if !ok {
		return nil, errors.New("replicated partition preflight requires a partition-aware fingerprint verifier")
	}
	observer, err := NewReplicaPartitionObserver(client, verifier)
	if err != nil {
		return nil, err
	}
	return &ReplicatedPartitionLabPreflightService{client: client, partition: partition, observer: observer}, nil
}

func (service *ReplicatedPartitionLabPreflightService) Check(ctx context.Context, pair PairParameters, sourceSnapshot, targetSnapshot Snapshot, sourceTable, targetTable Table, partition string) (ReplicatedPartitionLabPreflight, error) {
	pair, err := normalizePairParameters(pair)
	if err != nil {
		return ReplicatedPartitionLabPreflight{}, err
	}
	partition = strings.TrimSpace(partition)
	if partition == "" {
		return ReplicatedPartitionLabPreflight{}, errors.New("replicated partition preflight requires a partition ID")
	}
	if sourceTable.Database != pair.Database || targetTable.Database != pair.Database || sourceTable.Name == "" || targetTable.Name == "" {
		return ReplicatedPartitionLabPreflight{}, errors.New("preflight tables must belong to the frozen migration database")
	}
	report := ReplicatedPartitionLabPreflight{Database: pair.Database, Partition: partition, SourceTable: sourceTable.Name, TargetTable: targetTable.Name}

	if _, ok := findTable(sourceSnapshot, sourceTable.Database, sourceTable.Name); !ok {
		report.Findings = append(report.Findings, "source table is not present in the frozen discovery snapshot")
	}
	if _, ok := findTable(targetSnapshot, targetTable.Database, targetTable.Name); !ok {
		report.Findings = append(report.Findings, "target table is not present in the frozen discovery snapshot")
	}
	if sourceTable.IsDistributed || sourceTable.IsMaterializedView || sourceTable.IsReplicated {
		report.Findings = append(report.Findings, "source physical table must be a non-replicated MergeTree-family table for the first lab slice")
	}
	if !strings.HasSuffix(strings.ToLower(strings.TrimSpace(sourceTable.Engine)), "mergetree") {
		report.Findings = append(report.Findings, "source engine is not a MergeTree-family table")
	}
	if !targetTable.IsReplicated || !strings.HasSuffix(strings.ToLower(strings.TrimSpace(targetTable.Engine)), "mergetree") {
		report.Findings = append(report.Findings, "target must be a ReplicatedMergeTree-family table")
	}
	if !compatibleTransferColumns(sourceTable, targetTable) {
		report.Findings = append(report.Findings, "source and target insertable columns are not identical")
	}
	if normalizeExpression(sourceTable.PartitionKey) != normalizeExpression(targetTable.PartitionKey) {
		report.Findings = append(report.Findings, "source and target partition keys are different")
	}
	if normalizeExpression(sourceTable.SortingKey) != normalizeExpression(targetTable.SortingKey) {
		report.Findings = append(report.Findings, "source and target sorting keys are different")
	}
	if normalizeExpression(sourceTable.PrimaryKey) != normalizeExpression(targetTable.PrimaryKey) {
		report.Findings = append(report.Findings, "source and target primary keys are different")
	}

	capability, err := InspectReplicatedPartitionCapability(ctx, service.client, pair.Target, pair.Database, targetTable)
	if err != nil {
		return report, err
	}
	report.Capability = capability
	if !capability.ReadyForLab {
		report.Findings = append(report.Findings, "replicated partition capability is not ready: "+capability.Reason)
	}

	partitionMeta, ok := findPartition(sourceTable, partition)
	if !ok || partitionMeta.Rows == 0 {
		report.Findings = append(report.Findings, "source partition is absent or empty in the discovery snapshot")
	} else {
		report.SourceBytes = partitionMeta.BytesOnDisk
	}
	for _, mutation := range targetSnapshot.Mutations {
		if mutation.Database == pair.Database && mutation.Table == targetTable.Name && !mutation.IsDone {
			report.Findings = append(report.Findings, fmt.Sprintf("target has unfinished mutation %s", mutation.MutationID))
		}
	}
	if targetSnapshot.Topology.Shards != 1 {
		report.Findings = append(report.Findings, fmt.Sprintf("first replicated partition lab slice requires one shard, discovered %d", targetSnapshot.Topology.Shards))
	}

	sourceFingerprint, err := service.partition.FingerprintPartition(ctx, pair.Source, pair.Database, sourceTable, partition)
	if err != nil {
		return report, fmt.Errorf("fingerprint source partition during preflight: %w", err)
	}
	report.SourceFingerprint = sourceFingerprint
	if sourceFingerprint.Rows == 0 {
		report.Findings = append(report.Findings, "source partition fingerprint is empty")
	}

	targetReplicas, err := service.observer.ObserveAbsent(ctx, targetSnapshot, pair.Target, pair.Database, targetTable, partition, sourceFingerprint)
	if err != nil {
		return report, fmt.Errorf("observe target replicas during preflight: %w", err)
	}
	report.TargetReplicas = targetReplicas
	if targetReplicas.State != ReplicaPartitionConverged {
		report.Findings = append(report.Findings, "target partition is not proven absent and healthy on every expected replica")
	}

	report.Passed = len(report.Findings) == 0
	return report, nil
}

func compatibleTransferColumns(source, target Table) bool {
	sourceColumns, err := transferColumns(source)
	if err != nil {
		return false
	}
	targetColumns, err := transferColumns(target)
	if err != nil || len(sourceColumns) != len(targetColumns) {
		return false
	}
	sourceTypes := make(map[string]string, len(source.Columns))
	for _, column := range source.Columns {
		sourceTypes[column.Name] = column.Type
	}
	targetTypes := make(map[string]string, len(target.Columns))
	for _, column := range target.Columns {
		targetTypes[column.Name] = column.Type
	}
	for index := range sourceColumns {
		if sourceColumns[index] != targetColumns[index] || sourceTypes[sourceColumns[index]] != targetTypes[targetColumns[index]] {
			return false
		}
	}
	return true
}

func findPartition(table Table, partition string) (Partition, bool) {
	for _, item := range table.Partitions {
		if item.Partition == partition {
			return item, true
		}
	}
	return Partition{}, false
}
