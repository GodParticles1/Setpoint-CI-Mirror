package clickhouse

import (
	"context"
	"errors"
	"sync"
	"testing"
)

type memoryRestoreStore struct {
	mu      sync.Mutex
	records map[RestoreKey]RestoreRecord
	putErr  error
	failAt  int
	puts    int
}

func newMemoryRestoreStore() *memoryRestoreStore {
	return &memoryRestoreStore{records: make(map[RestoreKey]RestoreRecord)}
}

func (store *memoryRestoreStore) PutRestore(_ context.Context, record RestoreRecord) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.puts++
	if store.failAt > 0 && store.puts == store.failAt {
		return errors.New("injected restore persistence failure")
	}
	if store.putErr != nil {
		err := store.putErr
		store.putErr = nil
		return err
	}
	if err := ValidateRestoreRecord(record); err != nil {
		return err
	}
	if current, ok := store.records[record.Key]; ok {
		if current.OwnershipToken != record.OwnershipToken || current.Target != record.Target || current.Restore.Database != record.Restore.Database || current.Restore.Table != record.Restore.Table || current.Baseline != record.Baseline {
			return errors.New("restore frozen state changed")
		}
		if current.Restore.UUID != "" && current.Restore.UUID != record.Restore.UUID {
			return errors.New("restore UUID changed")
		}
		if current.Restore.Engine != "" && current.Restore.Engine != record.Restore.Engine {
			return errors.New("restore engine changed")
		}
		if current.Restore.SchemaFingerprint != "" && current.Restore.SchemaFingerprint != record.Restore.SchemaFingerprint {
			return errors.New("restore schema changed")
		}
		if err := ValidateRestoreTransition(current.State, record.State); err != nil {
			return err
		}
	} else if record.State != RestoreIntent {
		return errors.New("first restore record must be intent")
	}
	store.records[record.Key] = record
	return nil
}

func (store *memoryRestoreStore) GetRestore(_ context.Context, key RestoreKey) (RestoreRecord, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	record, ok := store.records[key]
	return record, ok, nil
}

func (store *memoryRestoreStore) ListRestores(_ context.Context, runID string) ([]RestoreRecord, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	records := make([]RestoreRecord, 0)
	for _, record := range store.records {
		if record.Key.RunID == runID {
			records = append(records, record)
		}
	}
	return records, nil
}

type memoryRestoreObjects struct {
	mu            sync.Mutex
	objects       map[string]RestoreObjectSnapshot
	fingerprints  map[string]DataFingerprint
	creates       int
	drops         int
	createErr     error
	partialCreate bool
}

func newMemoryRestoreObjects(t *testing.T, targets ...Table) *memoryRestoreObjects {
	t.Helper()
	objects := &memoryRestoreObjects{objects: make(map[string]RestoreObjectSnapshot), fingerprints: make(map[string]DataFingerprint)}
	for index, target := range targets {
		schema, err := tableSchemaFingerprint(target)
		if err != nil {
			t.Fatal(err)
		}
		objects.objects[target.Database+"."+target.Name] = RestoreObjectSnapshot{
			Exists:     true,
			Identity:   RestoreObjectIdentity{Database: target.Database, Table: target.Name, UUID: "target-uuid-" + string(rune('a'+index)), Engine: target.Engine, SchemaFingerprint: schema},
			Partitions: append([]Partition(nil), target.Partitions...),
		}
		objects.fingerprints[target.Database+"."+target.Name] = DataFingerprint{}
	}
	return objects
}

func (objects *memoryRestoreObjects) Inspect(_ context.Context, _ Endpoint, database, table string) (RestoreObjectSnapshot, error) {
	objects.mu.Lock()
	defer objects.mu.Unlock()
	snapshot, ok := objects.objects[database+"."+table]
	if !ok {
		return RestoreObjectSnapshot{}, nil
	}
	return snapshot, nil
}

func (objects *memoryRestoreObjects) Create(_ context.Context, _ Endpoint, database, restoreTable string, target Table) error {
	objects.mu.Lock()
	defer objects.mu.Unlock()
	key := database + "." + restoreTable
	if _, exists := objects.objects[key]; exists {
		return errors.New("restore object already exists")
	}
	schema, err := tableSchemaFingerprint(target)
	if err != nil {
		return err
	}
	baseline := objects.objects[database+"."+target.Name]
	objects.objects[key] = RestoreObjectSnapshot{
		Exists:     true,
		Identity:   RestoreObjectIdentity{Database: database, Table: restoreTable, UUID: "generated-" + restoreTable, Engine: target.Engine, SchemaFingerprint: schema},
		Partitions: append([]Partition(nil), baseline.Partitions...),
	}
	objects.fingerprints[key] = objects.fingerprints[database+"."+target.Name]
	objects.creates++
	if objects.createErr != nil {
		err := objects.createErr
		objects.createErr = nil
		if objects.partialCreate {
			objects.fingerprints[key] = DataFingerprint{Rows: 1, Bytes: 8, HashSum64: "10", HashXor64: "1"}
		} else {
			delete(objects.objects, key)
			delete(objects.fingerprints, key)
		}
		return err
	}
	return nil
}

func (objects *memoryRestoreObjects) Drop(_ context.Context, _ Endpoint, database, table string) error {
	objects.mu.Lock()
	defer objects.mu.Unlock()
	delete(objects.objects, database+"."+table)
	delete(objects.fingerprints, database+"."+table)
	objects.drops++
	return nil
}

func (objects *memoryRestoreObjects) Fingerprint(_ context.Context, _ Endpoint, database string, table Table, _ *TimeRangeFilter) (DataFingerprint, error) {
	objects.mu.Lock()
	defer objects.mu.Unlock()
	fingerprint, ok := objects.fingerprints[database+"."+table.Name]
	if !ok {
		return DataFingerprint{}, errors.New("table fingerprint is unavailable")
	}
	return fingerprint, nil
}

func (objects *memoryRestoreObjects) setFingerprint(database, table string, fingerprint DataFingerprint) {
	objects.mu.Lock()
	defer objects.mu.Unlock()
	objects.fingerprints[database+"."+table] = fingerprint
}

func newTestRestoreProvider(t *testing.T, ledger LedgerStore, staging StagingController, verifier FingerprintVerifier, commit *AtomicExchangeCommitEngine, targets ...Table) (*ExchangeRestoreProvider, *memoryRestoreStore, *memoryRestoreObjects) {
	t.Helper()
	store := newMemoryRestoreStore()
	objects := newMemoryRestoreObjects(t, targets...)
	provider, err := NewExchangeRestoreProvider(ledger, store, staging, objects, verifier, commit)
	if err != nil {
		t.Fatal(err)
	}
	return provider, store, objects
}
