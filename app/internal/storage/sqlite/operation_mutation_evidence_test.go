package sqlite

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"setpoint/internal/operation"
	"setpoint/internal/operationrun"
	"setpoint/internal/protocol"
	"setpoint/internal/task"
)

func failedRollbackSubmission(f *operationExecutionFixture) task.ResultSubmission {
	result := *f.submission.OperationExecutionResult
	result.Rollback = &operation.RollbackResult{Restored: false, MutationState: operation.MutationMayHaveChanged, Checkpoint: "partial_rollback", State: operation.Artifact{SchemaVersion: "test.rollback.v1", Payload: json.RawMessage(`{"restored":["owned-a"],"pending":["owned-b"]}`)}, Evidence: []operation.EvidenceRef{{ID: "partial-recovery", Kind: "rollback"}}}
	result.Error = &task.Failure{Code: "rollback_failed", Message: "partial recovery"}
	return task.ResultSubmission{ClaimID: f.claimID, Phase: task.PhaseFailed, OperationExecutionResult: &result}
}

func TestFailedRollbackProtocolSQLiteReopenRecoveryAndIdempotence(t *testing.T) {
	f := prepareOperationExecutionFixture(t, task.OperationActionRollback)
	defer func() { f.store.Close() }()
	before, err := f.store.GetOperationRun(f.ctx, f.runID)
	if err != nil {
		t.Fatal(err)
	}
	submission := failedRollbackSubmission(f)
	raw, err := json.Marshal(protocol.TaskResultRequest(submission))
	if err != nil {
		t.Fatal(err)
	}
	var decoded protocol.TaskResultRequest
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(submission, decoded) {
		t.Fatal("protocol discarded recovery evidence")
	}
	completed, err := f.store.CompleteTask(f.ctx, f.nodeID, f.taskID, decoded, f.reportedAt)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status.Phase != task.PhaseFailed || completed.Status.LastError == nil || !reflect.DeepEqual(completed.OperationExecutionResult, decoded.OperationExecutionResult) {
		t.Fatal("failed result not retained")
	}
	// Use SQLite's database identity to close and reopen the actual fixture DB.
	var sequence int
	var name, path string
	if err := f.store.db.QueryRow("PRAGMA database_list").Scan(&sequence, &name, &path); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	f.store, err = open(f.ctx, path, func() time.Time { return f.reportedAt.Add(time.Minute) })
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.store.LoadOperationRuntimeSnapshot(f.ctx, f.runID, f.reportedAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Run.Status.State != operation.StateRollingBack || !snapshot.WriteBlockedUntilReconcile || snapshot.Run.Status.Checkpoint != "action_rollback_failed" {
		t.Fatalf("unsafe recovery=%#v", snapshot)
	}
	if snapshot.Run.Execution == nil || !reflect.DeepEqual(snapshot.Run.Execution.Rollback, submission.OperationExecutionResult.Rollback) || !reflect.DeepEqual(snapshot.Run.Execution.Apply, before.Execution.Apply) || !reflect.DeepEqual(snapshot.Run.Execution.RestorePoint, before.Execution.RestorePoint) {
		t.Fatal("snapshot lost or replaced recovery facts")
	}
	stored, err := f.store.GetTask(f.ctx, f.taskID)
	if err != nil || !reflect.DeepEqual(stored.OperationExecutionResult, submission.OperationExecutionResult) {
		t.Fatalf("task roundtrip=%v", err)
	}
	count := countRows(t, f.store, `SELECT COUNT(*) FROM operation_journal WHERE run_id = ?`, f.runID)
	if _, err := f.store.CompleteTask(f.ctx, f.nodeID, f.taskID, decoded, f.reportedAt.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, f.store, `SELECT COUNT(*) FROM operation_journal WHERE run_id = ?`, f.runID); got != count {
		t.Fatal("retry duplicated journal")
	}
	decoded.OperationExecutionResult.Rollback.Checkpoint = "different_fact"
	if _, err := f.store.CompleteTask(f.ctx, f.nodeID, f.taskID, decoded, f.reportedAt.Add(3*time.Minute)); !errors.Is(err, task.ErrResultConflict) {
		t.Fatalf("conflicting retry=%v", err)
	}
}

func TestMutationEvidenceRejectedAtomically(t *testing.T) {
	tests := []struct {
		name   string
		action task.OperationAction
		mutate func(*task.ResultSubmission)
	}{
		{"failed_apply_invalid_state", task.OperationActionApply, func(s *task.ResultSubmission) {
			s.Phase = task.PhaseFailed
			s.OperationExecutionResult.Error = &task.Failure{Code: "apply_failed", Message: "failed"}
			s.OperationExecutionResult.Apply.MutationState = "invented"
		}},
		{"rollback_missing_state", task.OperationActionRollback, func(s *task.ResultSubmission) { s.OperationExecutionResult.Rollback.MutationState = "" }},
		{"rollback_invalid_state", task.OperationActionRollback, func(s *task.ResultSubmission) { s.OperationExecutionResult.Rollback.MutationState = "invented" }},
		{"rollback_optimistic", task.OperationActionRollback, func(s *task.ResultSubmission) { s.OperationExecutionResult.Rollback.Restored = true }},
		{"rollback_no_checkpoint", task.OperationActionRollback, func(s *task.ResultSubmission) { s.OperationExecutionResult.Rollback.Checkpoint = "" }},
		{"rollback_no_schema", task.OperationActionRollback, func(s *task.ResultSubmission) { s.OperationExecutionResult.Rollback.State.SchemaVersion = "" }},
		{"rollback_no_payload", task.OperationActionRollback, func(s *task.ResultSubmission) { s.OperationExecutionResult.Rollback.State.Payload = nil }},
		{"rollback_mixed", task.OperationActionRollback, func(s *task.ResultSubmission) { s.OperationExecutionResult.Apply = &operation.ApplyResult{} }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := prepareOperationExecutionFixture(t, tc.action)
			defer f.store.Close()
			if tc.action == task.OperationActionRollback {
				f.submission = failedRollbackSubmission(f)
			}
			before, err := f.store.GetOperationRun(f.ctx, f.runID)
			if err != nil {
				t.Fatal(err)
			}
			count := countRows(t, f.store, `SELECT COUNT(*) FROM operation_journal WHERE run_id = ?`, f.runID)
			tc.mutate(&f.submission)
			if _, err := f.store.CompleteTask(f.ctx, f.nodeID, f.taskID, f.submission, f.reportedAt); err == nil {
				t.Fatal("invalid evidence accepted")
			}
			assertRejectedOperationExecutionResultLeftNoWrites(t, f, before, count)
		})
	}
}

func TestReconnectServerRequiresTypedStateAndBootProof(t *testing.T) {
	for _, action := range []task.OperationAction{task.OperationActionApply, task.OperationActionRollback} {
		for _, mode := range []string{"missing_state", "invalid_state", "pending", "same_boot", "malformed_boot", "valid"} {
			t.Run(string(action)+"/"+mode, func(t *testing.T) {
				f := prepareOperationExecutionFixture(t, action)
				defer f.store.Close()
				handoff := &operation.ReconnectHandoff{Barrier: operation.StageBarrierAgentReconnect, Reboot: true, BootIDBefore: "before", BootIDAfter: "after"}
				state := operation.MutationChanged
				switch mode {
				case "missing_state":
					state = ""
				case "invalid_state":
					state = "invalid"
				case "pending":
					handoff.BootIDAfter = ""
				case "same_boot":
					handoff.BootIDAfter = "before"
				case "malformed_boot":
					handoff.BootIDBefore = "bad id"
				}
				if action == task.OperationActionApply {
					f.submission.OperationExecutionResult.Apply.MutationState = state
					f.submission.OperationExecutionResult.Apply.Reconnect = handoff
				} else {
					f.submission.OperationExecutionResult.Rollback.MutationState = state
					f.submission.OperationExecutionResult.Rollback.Reconnect = handoff
				}
				before, err := f.store.GetOperationRun(f.ctx, f.runID)
				if err != nil {
					t.Fatal(err)
				}
				count := countRows(t, f.store, `SELECT COUNT(*) FROM operation_journal WHERE run_id = ?`, f.runID)
				_, err = f.store.CompleteTask(f.ctx, f.nodeID, f.taskID, f.submission, f.reportedAt)
				if mode == "valid" {
					if err != nil {
						t.Fatal(err)
					}
					return
				}
				if err == nil {
					t.Fatal("invalid reconnect accepted")
				}
				assertRejectedOperationExecutionResultLeftNoWrites(t, f, before, count)
			})
		}
	}
}

func TestFailedRollbackTransactionFailureRetainsPriorSnapshot(t *testing.T) {
	f := prepareOperationExecutionFixture(t, task.OperationActionRollback)
	defer f.store.Close()
	f.submission = failedRollbackSubmission(f)
	before, err := f.store.GetOperationRun(f.ctx, f.runID)
	if err != nil {
		t.Fatal(err)
	}
	count := countRows(t, f.store, `SELECT COUNT(*) FROM operation_journal WHERE run_id = ?`, f.runID)
	if _, err := f.store.db.Exec(`CREATE TRIGGER sp105_fail_checkpoint BEFORE UPDATE OF checkpoint ON operation_runs BEGIN SELECT RAISE(ABORT, 'injected failure'); END;`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.CompleteTask(f.ctx, f.nodeID, f.taskID, f.submission, f.reportedAt); err == nil {
		t.Fatal("checkpoint failure ignored")
	}
	assertRejectedOperationExecutionResultLeftNoWrites(t, f, before, count)
}

func TestFailedRollbackStageSnapshotPreservesTypedEvidence(t *testing.T) {
	result := operation.RollbackResult{MutationState: operation.MutationNotStarted, Checkpoint: "pre_mutation_failure", State: operation.Artifact{SchemaVersion: "test.rollback.v1", Payload: json.RawMessage(`{}`)}}
	for _, nodes := range [][]string{{"node-a"}, {"node-a", "node-b"}} {
		contract := task.OperationExecutionContract{Action: task.OperationActionRollback, ParticipantNodeIDs: nodes, StageIndex: 1, Stage: operation.PlanStep{ID: "restore-stage", ExecutorNodeID: "node-a"}}
		snapshot := operationExecutionResultSnapshot(contract, task.PhaseFailed, task.OperationExecutionResult{Rollback: &result}, time.Now().UTC())
		if len(snapshot.Stages) != 1 || snapshot.Stages[0].StageIndex != 1 || !reflect.DeepEqual(snapshot.Stages[0].Rollback, &result) {
			t.Fatal("stage recovery facts lost")
		}
		if len(nodes) > 1 && snapshot.Rollback != nil {
			t.Fatal("multi-node evidence incorrectly flattened")
		}
	}
}

func TestLegacyFailedApplyTypedStateRemainsUnspecifiedAfterRecovery(t *testing.T) {
	f := prepareOperationExecutionFixture(t, task.OperationActionApply)
	defer f.store.Close()
	apply := validFailedApplyEvidence()
	f.submission.Phase = task.PhaseFailed
	f.submission.OperationExecutionResult.Apply = &apply
	f.submission.OperationExecutionResult.Error = &task.Failure{Code: "apply_failed", Message: "legacy failure"}
	if _, err := f.store.CompleteTask(f.ctx, f.nodeID, f.taskID, f.submission, f.reportedAt); err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.store.LoadOperationRuntimeSnapshot(f.ctx, f.runID, f.reportedAt)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snapshot.Run.Execution.Apply, &apply) || snapshot.Run.Execution.Apply.MutationState != "" {
		t.Fatal("legacy evidence changed or typed state inferred")
	}
	// Snapshot JSON and typed resource decoding must preserve the same empty state.
	raw, err := json.Marshal(snapshot.Run)
	if err != nil {
		t.Fatal(err)
	}
	var decoded operationrun.Resource
	if err := json.Unmarshal(raw, &decoded); err != nil || !reflect.DeepEqual(decoded.Execution.Apply, &apply) {
		t.Fatalf("snapshot roundtrip=%v", err)
	}
}
