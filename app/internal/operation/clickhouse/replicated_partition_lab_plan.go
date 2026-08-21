package clickhouse

import (
	"errors"
	"fmt"
	"strings"
)

// ReplicatedPartitionLabPlan is deliberately non-executable. It makes the
// candidate SQL and every required safety gate reviewable before the first
// physical lab run.
type ReplicatedPartitionLabPlan struct {
	RunID             string   `json:"run_id"`
	Database          string   `json:"database"`
	TargetTable       string   `json:"target_table"`
	StagingTable      string   `json:"staging_table"`
	Partition         string   `json:"partition"`
	CreateStagingSQL  string   `json:"create_staging_sql"`
	CommitSQL         string   `json:"commit_sql"`
	RollbackSQL       string   `json:"rollback_sql"`
	DropStagingSQL    string   `json:"drop_staging_sql"`
	Preconditions     []string `json:"preconditions"`
	Postconditions    []string `json:"postconditions"`
	ApplyEnabled      bool     `json:"apply_enabled"`
}

func BuildReplicatedPartitionLabPlan(runID, database string, target Table, partition, storagePolicy string) (ReplicatedPartitionLabPlan, error) {
	runID = strings.TrimSpace(runID)
	partition = strings.TrimSpace(partition)
	if runID == "" { return ReplicatedPartitionLabPlan{}, errors.New("lab plan run ID is required") }
	if !validIdentifier(database) || !validIdentifier(target.Name) { return ReplicatedPartitionLabPlan{}, errors.New("lab plan target identifiers are invalid") }
	if partition == "" { return ReplicatedPartitionLabPlan{}, errors.New("lab plan partition is required") }
	if !target.IsReplicated || !strings.HasSuffix(strings.ToLower(strings.TrimSpace(target.Engine)), "mergetree") {
		return ReplicatedPartitionLabPlan{}, errors.New("lab plan requires a ReplicatedMergeTree-family target")
	}
	staging, err := BuildStagingTableName(runID, target.Name)
	if err != nil { return ReplicatedPartitionLabPlan{}, err }
	createSQL, err := BuildReplicatedPartitionStagingDDL(database, staging, target.Name, target, storagePolicy)
	if err != nil { return ReplicatedPartitionLabPlan{}, err }
	commitSQL, err := BuildReplacePartitionSQL(database, target.Name, staging, partition)
	if err != nil { return ReplicatedPartitionLabPlan{}, err }
	rollbackSQL := fmt.Sprintf("ALTER TABLE %s.%s DROP PARTITION %s", quoteIdentifier(database), quoteIdentifier(target.Name), quoteLiteral(partition))
	dropStaging := fmt.Sprintf("DROP TABLE IF EXISTS %s.%s", quoteIdentifier(database), quoteIdentifier(staging))
	return ReplicatedPartitionLabPlan{
		RunID: runID, Database: database, TargetTable: target.Name, StagingTable: staging, Partition: partition,
		CreateStagingSQL: createSQL, CommitSQL: commitSQL, RollbackSQL: rollbackSQL, DropStagingSQL: dropStaging,
		Preconditions: []string{
			"target partition fingerprint is verified empty immediately before commit",
			"run owns a non-expired exclusive lock for the physical target table",
			"staging partition fingerprint equals the source partition fingerprint",
			"target replicas are healthy and there is no unfinished mutation for the table",
			"storage policy and partition/sorting/primary keys match the target requirements",
			"enforce_index_structure_match_on_partition_manipulation is disabled or exact index/projection parity is independently proven",
		},
		Postconditions: []string{
			"target partition fingerprint equals the verified source fingerprint",
			"replicated destination converges on every expected replica",
			"migration ledger is committed only after data and replica verification",
			"rollback may DROP the partition only while its current fingerprint still equals the run-owned committed fingerprint",
		},
		ApplyEnabled: false,
	}, nil
}
