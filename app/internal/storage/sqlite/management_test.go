package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"setpoint/internal/checkrun"
	"setpoint/internal/domain"
	"setpoint/internal/task"
	"setpoint/internal/trustedexec"
)

func TestSitesAndCheckRunsAreIdempotentAggregatedAndDurable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "setpoint.db")
	now := time.Date(2026, 8, 4, 9, 0, 0, 0, time.UTC)
	store, err := open(ctx, path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	seedTaskDependencies(t, store, "node-a", "test.read-only", now)

	siteRoots, err := trustedexec.NewConfiguredRoots(
		trustedexec.ScopeSite, "site:site-a", []string{"/opt/company/site/bin"},
	)
	if err != nil {
		t.Fatal(err)
	}
	site := domain.Site{ID: "site-a", Name: "测试站点", Description: "只读验证", TrustedExecutableRoots: siteRoots, CreatedAt: now, UpdatedAt: now}
	createdSite, created, err := store.CreateSite(ctx, site, "site-idem")
	if err != nil || !created || createdSite.ID != site.ID {
		t.Fatalf("create site=%#v created=%v err=%v", createdSite, created, err)
	}
	duplicateSite, created, err := store.CreateSite(ctx, site, "site-idem")
	if err != nil || created || duplicateSite.ID != site.ID {
		t.Fatalf("duplicate site=%#v created=%v err=%v", duplicateSite, created, err)
	}
	conflictingSite := site
	conflictingSite.ID = "site-b"
	conflictingSite.Description = "different"
	if _, _, err := store.CreateSite(ctx, conflictingSite, "site-idem"); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("site idempotency conflict=%v", err)
	}
	tags := []string{"linux", "canary"}
	notes := "read-only node"
	nodeRoots, err := trustedexec.NewConfiguredRoots(
		trustedexec.ScopeNode, "node:node-a", []string{"/opt/company/node/bin"},
	)
	if err != nil {
		t.Fatal(err)
	}
	updatedNode, err := store.UpdateNode(ctx, "node-a", domain.NodeUpdate{
		SiteID: &site.ID, Tags: &tags, Notes: &notes, TrustedExecutableRoots: &nodeRoots,
	})
	if err != nil || updatedNode.SiteID != site.ID || updatedNode.SiteName != site.Name ||
		len(updatedNode.Tags) != 2 || len(updatedNode.TrustedExecutableRoots) != 2 {
		t.Fatalf("updated node=%#v err=%v", updatedNode, err)
	}
	if err := store.DeleteSite(ctx, site.ID); !errors.Is(err, domain.ErrSiteNotEmpty) {
		t.Fatalf("delete populated site=%v", err)
	}

	run := checkrun.Resource{
		APIVersion: "setpoint.io/v1", Kind: "ReadOnlyCheckRun",
		Metadata: checkrun.Metadata{ID: "run-a", IdempotencyKey: "run-idem", Name: "baseline", CreatedAt: now},
		Spec: checkrun.Spec{
			NodeIDs: []string{"node-a"}, CheckIDs: []string{"test.read-only"},
			Parameters: map[string]json.RawMessage{"test.read-only": json.RawMessage(`{}`)},
		},
	}
	tasks := []task.Resource{taskResource("run-task-a", "run-task-idem-a", "node-a", "test.read-only", now)}
	createdRun, created, err := store.CreateCheckRun(ctx, run, tasks)
	if err != nil || !created || createdRun.Status.Phase != checkrun.PhasePending || len(createdRun.Tasks) != 1 {
		t.Fatalf("create run=%#v created=%v err=%v", createdRun, created, err)
	}
	duplicateRun, created, err := store.CreateCheckRun(ctx, run, tasks)
	if err != nil || created || duplicateRun.Metadata.ID != run.Metadata.ID {
		t.Fatalf("duplicate run=%#v created=%v err=%v", duplicateRun, created, err)
	}
	conflictingRun := run
	conflictingRun.Metadata.ID = "run-b"
	conflictingRun.Metadata.Name = "different"
	if _, _, err := store.CreateCheckRun(ctx, conflictingRun, tasks); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("run idempotency conflict=%v", err)
	}

	claimed, err := store.ClaimTask(ctx, "node-a", "claim-a", now.Add(time.Second))
	if err != nil || claimed == nil {
		t.Fatalf("claim task=%#v err=%v", claimed, err)
	}
	if _, err := store.AcknowledgeTask(ctx, "node-a", claimed.Metadata.ID, "claim-a", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	compliant := false
	result := checkResult("test.read-only", now.Add(2*time.Second))
	result.Items[0].Status = task.ItemUnsafe
	result.Items[0].Compliant = &compliant
	result.Items[0].Remediation = "review the configuration"
	if _, err := store.CompleteTask(ctx, "node-a", claimed.Metadata.ID, task.ResultSubmission{
		ClaimID: "claim-a", Phase: task.PhaseSucceeded, Result: &result,
	}, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	completedRun, err := store.GetCheckRun(ctx, run.Metadata.ID)
	if err != nil || completedRun.Status.Phase != checkrun.PhaseCompleted || completedRun.Status.Counts.Unsafe != 1 {
		t.Fatalf("completed run=%#v err=%v", completedRun, err)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	restored, err := store.GetCheckRun(ctx, run.Metadata.ID)
	if err != nil || restored.Status.Counts.Unsafe != 1 || len(restored.Tasks) != 1 || restored.Tasks[0].Result == nil {
		t.Fatalf("restored run=%#v err=%v", restored, err)
	}
	restoredNode, err := store.GetNode(ctx, "node-a", 0)
	if err != nil || len(restoredNode.TrustedExecutableRoots) != 2 {
		t.Fatalf("restored node=%#v err=%v", restoredNode, err)
	}
}

func TestCheckRunAggregateMarksMixedTerminalTasksPartialFailed(t *testing.T) {
	now := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	items := []task.Resource{
		{Status: task.Status{Phase: task.PhaseSucceeded, UpdatedAt: now}},
		{Status: task.Status{Phase: task.PhaseFailed, UpdatedAt: now.Add(time.Second)}},
	}
	status := checkrun.Aggregate(items)
	if status.Phase != checkrun.PhasePartialFailed || status.Counts.CompletedTasks != 2 {
		t.Fatalf("aggregate=%#v", status)
	}
}
