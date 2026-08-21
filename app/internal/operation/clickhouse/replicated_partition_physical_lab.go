package clickhouse

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
)

const replicatedPartitionPhysicalLabSchema = "clickhouse.replicated_partition_physical_lab.v1"

type ReplicatedPartitionPhysicalLabScenario string

const (
	LabScenarioBaseline           ReplicatedPartitionPhysicalLabScenario = "baseline_commit_and_rollback"
	LabScenarioAmbiguousCommit    ReplicatedPartitionPhysicalLabScenario = "ambiguous_commit_reconcile"
	LabScenarioRollbackDriftGuard ReplicatedPartitionPhysicalLabScenario = "rollback_drift_guard"
)

type ReplicatedPartitionPhysicalLabInput struct {
	BaseRunID      string
	SourceEndpoint Endpoint
	TargetEndpoint Endpoint
	SourceSnapshot Snapshot
	TargetSnapshot Snapshot
	Partition      string
}

type ReplicatedPartitionLabIsolation struct {
	OwnershipToken string `json:"ownership_token"`
	Database       string `json:"database"`
	SourceTable    string `json:"source_table"`
	TargetTable    string `json:"target_table"`
}

type ReplicatedPartitionLabScenarioPlan struct {
	Scenario                    ReplicatedPartitionPhysicalLabScenario `json:"scenario"`
	RunID                       string                                 `json:"run_id"`
	StagingTable                string                                 `json:"staging_table"`
	WriteAuthorization          string                                 `json:"write_authorization"`
	FaultInjection              string                                 `json:"fault_injection,omitempty"`
	RequiredLedgerStates        []LedgerState                          `json:"required_ledger_states"`
	AllowedIntermediateStates   []LedgerState                          `json:"allowed_intermediate_states,omitempty"`
	RequiredRollbackStates      []LedgerState                          `json:"required_rollback_states,omitempty"`
	AllowedRollbackIntermediate []LedgerState                          `json:"allowed_rollback_intermediate_states,omitempty"`
}

type ReplicatedPartitionPhysicalLabManifest struct {
	SchemaVersion        string                                `json:"schema_version"`
	BaseRunID            string                                `json:"base_run_id"`
	Partition            string                                `json:"partition"`
	SourceEndpoint       Endpoint                              `json:"source_endpoint"`
	TargetEndpoint       Endpoint                              `json:"target_endpoint"`
	ClickHouseVersion    string                                `json:"clickhouse_version"`
	Cluster              string                                `json:"cluster"`
	ExpectedReplicas     int                                   `json:"expected_replicas"`
	Isolation            ReplicatedPartitionLabIsolation       `json:"isolation"`
	Scenarios            []ReplicatedPartitionLabScenarioPlan  `json:"scenarios"`
	PreflightGates       []string                              `json:"preflight_gates"`
	EvidenceRequirements []string                              `json:"evidence_requirements"`
	CleanupRequirements  []string                              `json:"cleanup_requirements"`
	ProductApplyEnabled  bool                                  `json:"product_apply_enabled"`
}

func BuildReplicatedPartitionPhysicalLabManifest(input ReplicatedPartitionPhysicalLabInput) (ReplicatedPartitionPhysicalLabManifest, error) {
	baseRunID := strings.TrimSpace(input.BaseRunID)
	partition := strings.TrimSpace(input.Partition)
	if baseRunID == "" {
		return ReplicatedPartitionPhysicalLabManifest{}, errors.New("physical lab base run ID is required")
	}
	if partition == "" || len(partition) > 512 || strings.IndexByte(partition, 0) >= 0 {
		return ReplicatedPartitionPhysicalLabManifest{}, errors.New("physical lab partition ID is invalid")
	}
	if input.SourceEndpoint.Host == "" || input.TargetEndpoint.Host == "" {
		return ReplicatedPartitionPhysicalLabManifest{}, errors.New("physical lab source and target endpoints are required")
	}
	if input.SourceSnapshot.Version == "" || input.TargetSnapshot.Version == "" || input.SourceSnapshot.Version != input.TargetSnapshot.Version {
		return ReplicatedPartitionPhysicalLabManifest{}, errors.New("physical lab requires an exactly matching discovered ClickHouse version")
	}
	if input.SourceSnapshot.Topology.Mode != "single" || input.SourceSnapshot.Topology.Shards != 1 || input.SourceSnapshot.Topology.Replicas != 1 {
		return ReplicatedPartitionPhysicalLabManifest{}, errors.New("first physical lab source must be a proven single-node ClickHouse")
	}
	targets, err := expectedReplicaTargets(input.TargetSnapshot, input.TargetEndpoint)
	if err != nil {
		return ReplicatedPartitionPhysicalLabManifest{}, fmt.Errorf("validate physical lab target topology: %w", err)
	}
	cluster, err := physicalLabClusterName(input.TargetSnapshot)
	if err != nil {
		return ReplicatedPartitionPhysicalLabManifest{}, err
	}

	isolation, err := buildReplicatedPartitionLabIsolation(baseRunID)
	if err != nil {
		return ReplicatedPartitionPhysicalLabManifest{}, err
	}
	scenarios, err := buildReplicatedPartitionLabScenarios(baseRunID, isolation.TargetTable)
	if err != nil {
		return ReplicatedPartitionPhysicalLabManifest{}, err
	}

	return ReplicatedPartitionPhysicalLabManifest{
		SchemaVersion:     replicatedPartitionPhysicalLabSchema,
		BaseRunID:         baseRunID,
		Partition:         partition,
		SourceEndpoint:    input.SourceEndpoint,
		TargetEndpoint:    input.TargetEndpoint,
		ClickHouseVersion: input.SourceSnapshot.Version,
		Cluster:           cluster,
		ExpectedReplicas:  len(targets),
		Isolation:         isolation,
		Scenarios:         scenarios,
		PreflightGates: []string{
			"run uses only the generated run-owned lab database and tables; business tables are excluded",
			"source and target ClickHouse versions are discovered and exactly equal",
			"source is proven single-node; target is exactly one discovered cluster and one shard with at least two expected replicas",
			"replicated target table definition is created from environment-proven replication settings, never by copying another table's replica identity",
			"source and target insertable columns plus partition, sorting and primary keys are identical",
			"source partition fingerprint is non-empty and frozen before transfer",
			"target partition is absent and every expected replica is healthy immediately before commit",
			"target table has no unfinished mutation and target storage policy is proven",
			"Setpoint migration ledger and Agent/Server runtime use an isolated lab state store",
			"runtime credentials are not persisted in parameters, SQLite, journal, logs, URLs or argv",
		},
		EvidenceRequirements: []string{
			"capture source discovery snapshot and exact ClickHouse version before any lab write",
			"capture target cluster members, replica health, storage policy, table DDL metadata and mutation state before any lab write",
			"capture source partition row count and dual-hash fingerprint before transfer",
			"capture run-owned staging partition fingerprint before REPLACE PARTITION",
			"capture target replica report proving the partition absent before commit",
			"capture every ledger transition and checkpoint for the run",
			"capture the exact number of REPLACE PARTITION statements issued; each run must issue at most one",
			"capture every expected replica fingerprint and health state until commit converges",
			"capture rollback guard evidence immediately before DROP PARTITION",
			"capture the exact number of DROP PARTITION statements issued; each rollback run must issue at most one",
			"capture every expected replica proving partition absence after rollback",
			"record fault injection method and timing separately from normal execution evidence",
			"record cleanup verification proving all run-owned ClickHouse objects and isolated Setpoint lab state are gone",
		},
		CleanupRequirements: []string{
			"stop the isolated lab runner before removing its local SQLite/journal state",
			"drop only staging tables derived from the manifest run IDs",
			"drop only the generated run-owned target lab table/database after replica absence is verified",
			"drop only the generated run-owned source lab table/database",
			"query system.tables and system.databases to prove the run-owned objects no longer exist",
			"retain exported evidence outside the runtime state before cleanup",
		},
		ProductApplyEnabled: false,
	}, nil
}

func physicalLabClusterName(snapshot Snapshot) (string, error) {
	names := append([]string(nil), snapshot.Topology.ClusterNames...)
	if len(names) == 0 {
		unique := make(map[string]struct{})
		for _, member := range snapshot.Clusters {
			name := strings.TrimSpace(member.Cluster)
			if name != "" {
				unique[name] = struct{}{}
			}
		}
		for name := range unique {
			names = append(names, name)
		}
		sort.Strings(names)
	}
	if len(names) != 1 || strings.TrimSpace(names[0]) == "" {
		return "", fmt.Errorf("physical lab target must resolve exactly one cluster name, got %d", len(names))
	}
	return names[0], nil
}

func buildReplicatedPartitionLabIsolation(baseRunID string) (ReplicatedPartitionLabIsolation, error) {
	token := physicalLabToken(baseRunID)
	database := "sp_lab_" + token
	if !validIdentifier(database) {
		return ReplicatedPartitionLabIsolation{}, errors.New("generated physical lab database is invalid")
	}
	return ReplicatedPartitionLabIsolation{
		OwnershipToken: token,
		Database:       database,
		SourceTable:    "source_mt",
		TargetTable:    "target_rmt",
	}, nil
}

func buildReplicatedPartitionLabScenarios(baseRunID, targetTable string) ([]ReplicatedPartitionLabScenarioPlan, error) {
	templates := []struct {
		scenario                    ReplicatedPartitionPhysicalLabScenario
		suffix                      string
		fault                       string
		required                    []LedgerState
		allowedIntermediate         []LedgerState
		requiredRollback            []LedgerState
		allowedRollbackIntermediate []LedgerState
	}{
		{
			scenario:            LabScenarioBaseline,
			suffix:              "baseline",
			required:            []LedgerState{LedgerVerified, LedgerCommitPending, LedgerCommitted},
			allowedIntermediate: []LedgerState{LedgerReplicasConverging},
			requiredRollback:    []LedgerState{LedgerCommitted, LedgerRollbackPending, LedgerRolledBack},
		},
		{
			scenario:            LabScenarioAmbiguousCommit,
			suffix:              "ambiguous",
			fault:               "process-local client timeout/cancel after REPLACE PARTITION dispatch; do not stop ClickHouse, replication or networking",
			required:            []LedgerState{LedgerVerified, LedgerCommitPending, LedgerCommitUnknown, LedgerCommitted},
			allowedIntermediate: []LedgerState{LedgerReplicasConverging},
			requiredRollback:    []LedgerState{LedgerCommitted, LedgerRollbackPending, LedgerRolledBack},
		},
		{
			scenario:         LabScenarioRollbackDriftGuard,
			suffix:           "drift",
			fault:            "after committed convergence, add a deliberate extra row only to the isolated run-owned target partition; requires separate explicit fault-injection authorization",
			required:         []LedgerState{LedgerVerified, LedgerCommitPending, LedgerCommitted},
			allowedIntermediate: []LedgerState{LedgerReplicasConverging},
			requiredRollback: []LedgerState{LedgerCommitted, LedgerRollbackBlocked},
		},
	}
	result := make([]ReplicatedPartitionLabScenarioPlan, 0, len(templates))
	for _, template := range templates {
		runID := baseRunID + "-" + template.suffix
		staging, err := BuildStagingTableName(runID, targetTable)
		if err != nil {
			return nil, err
		}
		result = append(result, ReplicatedPartitionLabScenarioPlan{
			Scenario:                    template.scenario,
			RunID:                       runID,
			StagingTable:                staging,
			WriteAuthorization:          "explicit L3 authorization required; fault injection additionally requires explicit L4 authorization",
			FaultInjection:              template.fault,
			RequiredLedgerStates:        append([]LedgerState(nil), template.required...),
			AllowedIntermediateStates:   append([]LedgerState(nil), template.allowedIntermediate...),
			RequiredRollbackStates:      append([]LedgerState(nil), template.requiredRollback...),
			AllowedRollbackIntermediate: append([]LedgerState(nil), template.allowedRollbackIntermediate...),
		})
	}
	return result, nil
}

func physicalLabToken(baseRunID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(baseRunID)))
	return hex.EncodeToString(sum[:])[:10]
}
