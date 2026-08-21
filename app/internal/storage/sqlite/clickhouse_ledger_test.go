package sqlite

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"setpoint/internal/operation/clickhouse"
)

func TestClickHouseLedgerPersistsAndUpdatesAcrossReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "setpoint.db")
	store, err := Open(ctx, path)
	if err != nil { t.Fatal(err) }
	entry := clickhouse.LedgerEntry{
		Key: clickhouse.LedgerKey{RunID: "run-1", Database: "message_center", Table: "alarm", Partition: "202608", Chunk: 1},
		Strategy: clickhouse.StrategyNativeStream, State: clickhouse.LedgerPlanned, Attempt: 1,
		StagingTable: "spmig_alarm_123", Source: clickhouse.DataFingerprint{Rows: 10, HashSum64: "100", HashXor64: "7"},
		UpdatedAt: time.Date(2026, 8, 7, 7, 0, 0, 0, time.UTC),
	}
	if err := store.Put(ctx, entry); err != nil { t.Fatal(err) }
	entry.State = clickhouse.LedgerStaging
	entry.Checkpoint = "staging_ready"
	entry.UpdatedAt = entry.UpdatedAt.Add(time.Minute)
	if err := store.Put(ctx, entry); err != nil { t.Fatal(err) }
	entry.State = clickhouse.LedgerTransferred
	entry.Checkpoint = "native_bytes=1024"
	entry.UpdatedAt = entry.UpdatedAt.Add(time.Minute)
	if err := store.Put(ctx, entry); err != nil { t.Fatal(err) }
	entry.State = clickhouse.LedgerVerified
	entry.Target = entry.Source
	entry.UpdatedAt = entry.UpdatedAt.Add(time.Minute)
	if err := store.Put(ctx, entry); err != nil { t.Fatal(err) }
	if err := store.Close(); err != nil { t.Fatal(err) }

	store, err = Open(ctx, path)
	if err != nil { t.Fatal(err) }
	defer store.Close()
	got, ok, err := store.Get(ctx, entry.Key)
	if err != nil || !ok { t.Fatalf("get ok=%v err=%v", ok, err) }
	if got.State != clickhouse.LedgerVerified || got.Target.Rows != 10 || got.Attempt != 1 { t.Fatalf("got=%#v", got) }
	listed, err := store.ListRun(ctx, "run-1")
	if err != nil { t.Fatal(err) }
	if len(listed) != 1 || listed[0].Key.Partition != "202608" { t.Fatalf("listed=%#v", listed) }
}

func TestClickHouseLedgerPreservesCurrentState(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "setpoint.db"))
	if err != nil { t.Fatal(err) }
	defer store.Close()

	baseTime := time.Date(2026, 8, 13, 8, 0, 0, 0, time.UTC)
	entry := clickhouse.LedgerEntry{
		Key: clickhouse.LedgerKey{RunID: "run-order", Database: "db", Table: "events", Chunk: 1},
		Strategy: clickhouse.StrategyNativeStream, State: clickhouse.LedgerPlanned, Attempt: 1,
		StagingTable: "spmig_events_123", UpdatedAt: baseTime,
	}
	if err := store.Put(ctx, entry); err != nil { t.Fatal(err) }
	old := entry
	entry.State = clickhouse.LedgerStaging
	entry.Checkpoint = "staging_ready"
	entry.UpdatedAt = baseTime.Add(time.Minute)
	if err := store.Put(ctx, entry); err != nil { t.Fatal(err) }
	old.UpdatedAt = baseTime.Add(2 * time.Minute)
	if err := store.Put(ctx, old); err == nil {
		t.Fatal("earlier lifecycle state unexpectedly replaced current state")
	}
	got, ok, err := store.Get(ctx, entry.Key)
	if err != nil || !ok || got.State != clickhouse.LedgerStaging || got.Checkpoint != "staging_ready" {
		t.Fatalf("got=%#v ok=%v err=%v", got, ok, err)
	}
}

func TestClickHouseLedgerReadRejectsCorruptedPersistedState(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "setpoint.db"))
	if err != nil { t.Fatal(err) }
	defer store.Close()

	entry := clickhouse.LedgerEntry{
		Key: clickhouse.LedgerKey{RunID: "run-corrupt", Database: "db", Table: "events", Chunk: 1},
		Strategy: clickhouse.StrategyNativeStream,
		State: clickhouse.LedgerPlanned,
		Attempt: 1,
		UpdatedAt: time.Date(2026, 8, 13, 6, 0, 0, 0, time.UTC),
	}
	if err := store.Put(ctx, entry); err != nil { t.Fatal(err) }

	if _, err := store.db.ExecContext(ctx,
		`UPDATE clickhouse_migration_ledger SET state = ? WHERE run_id = ?`,
		"corrupt_state", entry.Key.RunID); err != nil {
		t.Fatal(err)
	}

	if _, ok, err := store.Get(ctx, entry.Key); err == nil || ok || !strings.Contains(err.Error(), "unsupported ClickHouse ledger state") {
		t.Fatalf("get ok=%v err=%v", ok, err)
	}
	if _, err := store.ListRun(ctx, entry.Key.RunID); err == nil || !strings.Contains(err.Error(), "unsupported ClickHouse ledger state") {
		t.Fatalf("list err=%v", err)
	}
}

func TestClickHouseLedgerReadRejectsCorruptedPersistedStrategy(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "setpoint.db"))
	if err != nil { t.Fatal(err) }
	defer store.Close()

	entry := clickhouse.LedgerEntry{
		Key: clickhouse.LedgerKey{RunID: "run-corrupt-strategy", Database: "db", Table: "events", Chunk: 1},
		Strategy: clickhouse.StrategyNativeStream,
		State: clickhouse.LedgerPlanned,
		Attempt: 1,
		UpdatedAt: time.Date(2026, 8, 13, 6, 5, 0, 0, time.UTC),
	}
	if err := store.Put(ctx, entry); err != nil { t.Fatal(err) }

	if _, err := store.db.ExecContext(ctx,
		`UPDATE clickhouse_migration_ledger SET strategy = ? WHERE run_id = ?`,
		"corrupt_strategy", entry.Key.RunID); err != nil {
		t.Fatal(err)
	}

	if _, ok, err := store.Get(ctx, entry.Key); err == nil || ok || !strings.Contains(err.Error(), "unsupported ClickHouse ledger strategy") {
		t.Fatalf("get ok=%v err=%v", ok, err)
	}
	if _, err := store.ListRun(ctx, entry.Key.RunID); err == nil || !strings.Contains(err.Error(), "unsupported ClickHouse ledger strategy") {
		t.Fatalf("list err=%v", err)
	}
}
