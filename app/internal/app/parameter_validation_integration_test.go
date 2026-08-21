package app_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	application "setpoint/internal/app"
	"setpoint/internal/domain"
	"setpoint/internal/plugin"
	"setpoint/internal/plugins"
	"setpoint/internal/plugins/linuxnetwork"
	"setpoint/internal/plugins/linuxpassword"
	"setpoint/internal/plugins/nginxbaseline"
	"setpoint/internal/plugins/sshbaseline"
	"setpoint/internal/protocol"
	storage "setpoint/internal/storage/sqlite"
	"setpoint/internal/task"
)

func TestSingleTaskRejectsInvalidParameterBeforePersistence(t *testing.T) {
	ctx, service := newParameterValidationService(t)
	_, created, err := service.CreateTask(ctx, protocol.CreateTaskRequest{
		APIVersion: "setpoint.io/v1", Kind: task.KindReadOnlyCheckTask,
		Metadata: protocol.CreateTaskMetadata{IdempotencyKey: "invalid-single-parameter"},
		Spec: task.Spec{
			NodeID: "parameter-node", PluginID: linuxnetwork.ID,
			Parameters: json.RawMessage(`{"host_role":123}`),
		},
	})
	if err == nil || created || !application.IsValidationError(err) {
		t.Fatalf("invalid single task created=%v err=%v", created, err)
	}
	if !strings.Contains(err.Error(), "host_role") || strings.Contains(err.Error(), "123") {
		t.Fatalf("unsafe or unlocatable validation error: %v", err)
	}
	tasks, err := service.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Fatalf("invalid task reached persistence: %#v", tasks)
	}
}

func TestCheckRunRejectsInvalidParameterBeforePersistence(t *testing.T) {
	ctx, service := newParameterValidationService(t)
	_, created, err := service.CreateCheckRun(ctx, protocol.CreateCheckRunRequest{
		APIVersion: "setpoint.io/v1", Kind: "ReadOnlyCheckRun",
		Metadata: protocol.CreateCheckRunMetadata{IdempotencyKey: "invalid-run-parameter"},
		Spec: protocol.CreateCheckRunSpec{
			NodeIDs: []string{"parameter-node"},
			CheckIDs: []string{"net.ipv4.conf.all.accept_source_route"},
			Parameters: map[string]json.RawMessage{
				linuxnetwork.ID: json.RawMessage(`{"host_role":"whatever"}`),
			},
		},
	})
	if err == nil || created || !application.IsValidationError(err) {
		t.Fatalf("invalid check run created=%v err=%v", created, err)
	}
	if !strings.Contains(err.Error(), "host_role") || strings.Contains(err.Error(), "whatever") {
		t.Fatalf("unsafe or unlocatable validation error: %v", err)
	}
	runs, _, err := service.ListCheckRuns(ctx, protocol.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("invalid run reached persistence: %#v", runs)
	}
	tasks, err := service.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Fatalf("invalid run dispatched tasks: %#v", tasks)
	}
}

func TestApplicationParameterGuardLeavesPluginSpecificSemanticsWithPlugins(t *testing.T) {
	ctx, service := newParameterValidationService(t)
	run, created, err := service.CreateCheckRun(ctx, protocol.CreateCheckRunRequest{
		APIVersion: "setpoint.io/v1", Kind: "ReadOnlyCheckRun",
		Metadata: protocol.CreateCheckRunMetadata{IdempotencyKey: "nginx-grammar-owned-by-plugin"},
		Spec: protocol.CreateCheckRunSpec{
			NodeIDs: []string{"parameter-node"}, CheckIDs: []string{"nginx.cors.allow_origin"},
			Parameters: map[string]json.RawMessage{
				nginxbaseline.ID: json.RawMessage(`{"cors_allowed_origins":"not-an-origin"}`),
			},
		},
	})
	if err != nil || !created || len(run.Tasks) != 1 {
		t.Fatalf("Nginx structured string was rejected by generic validation: created=%v run=%#v err=%v", created, run, err)
	}
	createdTask, created, err := service.CreateTask(ctx, protocol.CreateTaskRequest{
		APIVersion: "setpoint.io/v1", Kind: task.KindReadOnlyCheckTask,
		Metadata: protocol.CreateTaskMetadata{IdempotencyKey: "password-range-owned-by-plugin"},
		Spec: task.Spec{
			NodeID: "parameter-node", PluginID: linuxpassword.ID,
			Parameters: json.RawMessage(`{"pwquality_min_length_target":999}`),
		},
	})
	if err != nil || !created || createdTask.Spec.Execution == nil {
		t.Fatalf("out-of-range integer was incorrectly promoted into generic validation: created=%v err=%v", created, err)
	}
}

func TestSingleTaskCanonicalParametersKeepFreezeDigestStable(t *testing.T) {
	ctx, service := newParameterValidationService(t)
	first, created, err := service.CreateTask(ctx, protocol.CreateTaskRequest{
		APIVersion: "setpoint.io/v1", Kind: task.KindReadOnlyCheckTask,
		Metadata: protocol.CreateTaskMetadata{IdempotencyKey: "canonical-parameters-a"},
		Spec: task.Spec{
			NodeID: "parameter-node", PluginID: sshbaseline.ID,
			Parameters: json.RawMessage(`{"permit_root_login_target":"no","password_authentication_target":"yes"}`),
		},
	})
	if err != nil || !created {
		t.Fatalf("create first canonical task: created=%v err=%v", created, err)
	}
	second, created, err := service.CreateTask(ctx, protocol.CreateTaskRequest{
		APIVersion: "setpoint.io/v1", Kind: task.KindReadOnlyCheckTask,
		Metadata: protocol.CreateTaskMetadata{IdempotencyKey: "canonical-parameters-b"},
		Spec: task.Spec{
			NodeID: "parameter-node", PluginID: sshbaseline.ID,
			Parameters: json.RawMessage(`{"password_authentication_target":"yes","permit_root_login_target":"no"}`),
		},
	})
	if err != nil || !created {
		t.Fatalf("create second canonical task: created=%v err=%v", created, err)
	}
	if string(first.Spec.Parameters) != string(second.Spec.Parameters) || first.Spec.ContractDigest != second.Spec.ContractDigest {
		t.Fatalf("canonical parameters or frozen digest drifted: first=%s/%s second=%s/%s",
			first.Spec.Parameters, first.Spec.ContractDigest, second.Spec.Parameters, second.Spec.ContractDigest)
	}
}

func newParameterValidationService(t *testing.T) (context.Context, *application.Service) {
	t.Helper()
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "setpoint.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	registry := plugin.NewCheckRegistry()
	if err := plugins.RegisterFormal(registry); err != nil {
		t.Fatal(err)
	}
	service, err := application.NewService(store, store, registry, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SyncChecks(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RegisterNode(ctx, domain.Registration{
		AgentID: "parameter-node", Hostname: "parameter-node", OS: "linux", OSVersion: "test",
		Arch: "amd64", AgentVersion: "test", ReceivedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	return ctx, service
}
