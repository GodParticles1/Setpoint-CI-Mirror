package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
)

func isPhase3SchemaLineage(ctx context.Context, transaction *sql.Tx, actual string) (bool, error) {
	version, err := strconv.Atoi(actual)
	if err != nil || version < 4 || version > 9 {
		return false, nil
	}
	ledger, err := sqliteTableExists(ctx, transaction, "clickhouse_migration_ledger")
	if err != nil || !ledger {
		return false, err
	}
	if version < 9 {
		return true, nil
	}
	journal, err := sqliteTableExists(ctx, transaction, "operation_journal")
	if err != nil {
		return false, err
	}
	checkpoints, err := sqliteTableExists(ctx, transaction, "operation_checkpoints")
	if err != nil {
		return false, err
	}
	restorePoints, err := sqliteTableExists(ctx, transaction, "clickhouse_restore_points")
	if err != nil {
		return false, err
	}
	switch {
	case journal && restorePoints && !checkpoints:
		return true, nil
	case checkpoints && !journal && !restorePoints:
		return false, nil
	default:
		return false, fmt.Errorf("ambiguous schema v9 lineage (journal=%t checkpoints=%t restore_points=%t)", journal, checkpoints, restorePoints)
	}
}

func migratePhase3SchemaLineage(ctx context.Context, transaction *sql.Tx, actual string) error {
	version, err := strconv.Atoi(actual)
	if err != nil || version < 4 || version > 9 {
		return fmt.Errorf("unsupported Phase 3 schema version %q", actual)
	}

	// Add the RC1 Check contract, selection, and trusted-root state without
	// rebuilding Phase 3 Operation tables or changing their durable records.
	if err := migrateSchemaV4(ctx, transaction); err != nil {
		return fmt.Errorf("add frozen Check contracts: %w", err)
	}
	if err := migrateSchemaV5(ctx, transaction); err != nil {
		return fmt.Errorf("add Check run selections: %w", err)
	}
	for _, statement := range schemaV8Statements {
		if _, err := transaction.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("add trusted executable roots: %w", err)
		}
	}

	if version == 4 {
		if err := migrateSchemaV10Core(ctx, transaction); err != nil {
			return fmt.Errorf("add Operation planning task schema: %w", err)
		}
		version = 5
	}
	for version < 9 {
		switch version {
		case 5:
			if err := applySchemaStatements(ctx, transaction, schemaV11Statements); err != nil {
				return fmt.Errorf("add Operation execution snapshot: %w", err)
			}
		case 6:
			if err := applySchemaStatements(ctx, transaction, schemaV12Statements); err != nil {
				return fmt.Errorf("add Operation journal: %w", err)
			}
		case 7:
			if err := applySchemaStatements(ctx, transaction, schemaV13Statements); err != nil {
				return fmt.Errorf("add Operation leases: %w", err)
			}
		case 8:
			if err := applySchemaStatements(ctx, transaction, schemaV14Statements); err != nil {
				return fmt.Errorf("add ClickHouse restore points: %w", err)
			}
		}
		version++
	}
	return nil
}

func applySchemaStatements(ctx context.Context, transaction *sql.Tx, statements []string) error {
	for _, statement := range statements {
		if _, err := transaction.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func sqliteTableExists(ctx context.Context, transaction *sql.Tx, table string) (bool, error) {
	var exists int
	if err := transaction.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?)`, table).Scan(&exists); err != nil {
		return false, fmt.Errorf("inspect table %s: %w", table, err)
	}
	return exists == 1, nil
}
