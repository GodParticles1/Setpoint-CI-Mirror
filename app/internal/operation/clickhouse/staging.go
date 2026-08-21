package clickhouse

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

type TransferChunk struct {
	RunID          string           `json:"run_id"`
	Strategy       StrategyID       `json:"strategy"`
	SourceDatabase string           `json:"source_database"`
	SourceTable    string           `json:"source_table"`
	TargetDatabase string           `json:"target_database"`
	TargetTable    string           `json:"target_table"`
	StagingTable   string           `json:"staging_table"`
	Partition      string           `json:"partition,omitempty"`
	Filter         *TimeRangeFilter `json:"filter,omitempty"`
	Sequence       uint64           `json:"sequence"`
}

func BuildStagingTableName(runID, table string) (string, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return "", errors.New("run ID is required")
	}
	if !validIdentifier(table) {
		return "", errors.New("table must be a simple identifier")
	}
	base := table
	if len(base) > 32 {
		base = base[:32]
	}
	sum := sha256.Sum256([]byte(runID + "|" + table))
	return fmt.Sprintf("spmig_%s_%s", base, hex.EncodeToString(sum[:])[:12]), nil
}

func ValidateTransferChunk(chunk TransferChunk) error {
	if strings.TrimSpace(chunk.RunID) == "" {
		return errors.New("transfer chunk run ID is required")
	}
	if chunk.Strategy == "" {
		return errors.New("transfer chunk strategy is required")
	}
	if !validIdentifier(chunk.SourceDatabase) || !validIdentifier(chunk.SourceTable) || !validIdentifier(chunk.TargetDatabase) || !validIdentifier(chunk.TargetTable) || !validIdentifier(chunk.StagingTable) {
		return errors.New("transfer chunk database/table names must be simple identifiers")
	}
	if chunk.Sequence == 0 {
		return errors.New("transfer chunk sequence must start at 1")
	}
	partition := strings.TrimSpace(chunk.Partition)
	if partition != "" {
		if chunk.Filter != nil {
			return errors.New("transfer chunk cannot combine partition ID and time-range filter")
		}
		if len(partition) > 512 || strings.IndexByte(partition, 0) >= 0 {
			return errors.New("transfer chunk partition ID is invalid")
		}
	}
	if chunk.Filter != nil {
		if _, err := chunk.Filter.whereClause(); err != nil {
			return err
		}
	}
	return nil
}

func transferWhereClause(chunk TransferChunk) (string, error) {
	if err := ValidateTransferChunk(chunk); err != nil {
		return "", err
	}
	if partition := strings.TrimSpace(chunk.Partition); partition != "" {
		return " WHERE _partition_id = " + quoteLiteral(partition), nil
	}
	return chunk.Filter.whereClause()
}

type StagingController interface {
	Recreate(context.Context, Endpoint, string, string, string) error
	Drop(context.Context, Endpoint, string, string) error
}

type SQLStagingController struct {
	client QueryClient
}

func NewSQLStagingController(client QueryClient) (*SQLStagingController, error) {
	if client == nil {
		return nil, errors.New("ClickHouse query client is required")
	}
	return &SQLStagingController{client: client}, nil
}

func (controller *SQLStagingController) Recreate(ctx context.Context, endpoint Endpoint, database, stagingTable, targetTable string) error {
	if !validIdentifier(database) || !validIdentifier(stagingTable) || !validIdentifier(targetTable) {
		return errors.New("staging identifiers are invalid")
	}
	engine, err := controller.client.Query(ctx, queryForEndpoint(endpoint, database,
		fmt.Sprintf("SELECT engine FROM system.tables WHERE database = %s AND name = %s", quoteLiteral(database), quoteLiteral(targetTable)), FormatTSVRaw))
	if err != nil {
		return fmt.Errorf("inspect target table before staging: %w", err)
	}
	if !safeExchangeTableEngine(strings.TrimSpace(engine)) {
		return fmt.Errorf("staging by CREATE TABLE AS is blocked for target engine %q; replicated/distributed targets require a dedicated staging strategy", strings.TrimSpace(engine))
	}
	if _, err := controller.client.Query(ctx, queryForEndpoint(endpoint, database,
		fmt.Sprintf("DROP TABLE IF EXISTS %s.%s", quoteIdentifier(database), quoteIdentifier(stagingTable)), FormatTSVRaw)); err != nil {
		return fmt.Errorf("drop previous staging table: %w", err)
	}
	query := fmt.Sprintf("CREATE TABLE %s.%s AS %s.%s", quoteIdentifier(database), quoteIdentifier(stagingTable), quoteIdentifier(database), quoteIdentifier(targetTable))
	if _, err := controller.client.Query(ctx, queryForEndpoint(endpoint, database, query, FormatTSVRaw)); err != nil {
		return fmt.Errorf("create staging table: %w", err)
	}
	return nil
}

func (controller *SQLStagingController) Exists(ctx context.Context, endpoint Endpoint, database, stagingTable string) (bool, error) {
	if !validIdentifier(database) || !validIdentifier(stagingTable) {
		return false, errors.New("staging identifiers are invalid")
	}
	raw, err := controller.client.Query(ctx, queryForEndpoint(endpoint, database,
		fmt.Sprintf("SELECT count() FROM system.tables WHERE database = %s AND name = %s", quoteLiteral(database), quoteLiteral(stagingTable)), FormatTSVRaw))
	if err != nil {
		return false, fmt.Errorf("inspect staging table existence: %w", err)
	}
	switch strings.TrimSpace(raw) {
	case "0":
		return false, nil
	case "1":
		return true, nil
	default:
		return false, fmt.Errorf("unexpected staging existence result %q", strings.TrimSpace(raw))
	}
}

func (controller *SQLStagingController) Drop(ctx context.Context, endpoint Endpoint, database, stagingTable string) error {
	if !validIdentifier(database) || !validIdentifier(stagingTable) {
		return errors.New("staging identifiers are invalid")
	}
	_, err := controller.client.Query(ctx, queryForEndpoint(endpoint, database,
		fmt.Sprintf("DROP TABLE IF EXISTS %s.%s", quoteIdentifier(database), quoteIdentifier(stagingTable)), FormatTSVRaw))
	if err != nil {
		return fmt.Errorf("drop staging table: %w", err)
	}
	return nil
}

func queryForEndpoint(endpoint Endpoint, database, query string, format QueryFormat) QueryRequest {
	return QueryRequest{Host: endpoint.Host, Port: endpoint.Port, User: endpoint.User, Secure: endpoint.Secure, Database: database, Query: query, Format: format}
}
