package app_test

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	application "setpoint/internal/app"
	"setpoint/internal/domain"
	"setpoint/internal/plugin"
	"setpoint/internal/plugins"
	"setpoint/internal/plugins/linuxbaseline"
	"setpoint/internal/protocol"
	storage "setpoint/internal/storage/sqlite"
)

func TestGranularCheckSelectionIsFrozenAndDurable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "setpoint.db")
	store, err := storage.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
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
		AgentID: "granular-node", Hostname: "granular-node", OS: "linux", OSVersion: "test",
		Arch: "amd64", AgentVersion: "test", ReceivedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	run, created, err := service.CreateCheckRun(ctx, protocol.CreateCheckRunRequest{
		APIVersion: "setpoint.io/v1", Kind: "ReadOnlyCheckRun",
		Metadata: protocol.CreateCheckRunMetadata{IdempotencyKey: "granular-run", Name: "one check"},
		Spec: protocol.CreateCheckRunSpec{
			NodeIDs: []string{"granular-node"}, CheckIDs: []string{"shell.umask"},
		},
	})
	if err != nil || !created {
		t.Fatalf("create run created=%v err=%v", created, err)
	}
	if !reflect.DeepEqual(run.Spec.CheckIDs, []string{"shell.umask"}) || len(run.Tasks) != 1 {
		t.Fatalf("run=%#v", run)
	}
	contract := run.Tasks[0].Spec.Execution
	if run.Tasks[0].Spec.PluginID != linuxbaseline.ID || contract == nil ||
		len(contract.Checks) != 1 || contract.Checks[0].ID != "shell.umask" {
		t.Fatalf("task contract=%#v", run.Tasks[0].Spec)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := storage.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restored, err := reopened.GetCheckRun(ctx, run.Metadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored.Spec.CheckIDs, []string{"shell.umask"}) || len(restored.Tasks) != 1 ||
		restored.Tasks[0].Spec.ContractDigest != run.Tasks[0].Spec.ContractDigest {
		t.Fatalf("restored=%#v", restored)
	}
}
