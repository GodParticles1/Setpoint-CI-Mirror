package clickhouse

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const restoreOwnershipTokenBytes = 16

type RestoreObjectSnapshot struct {
	Exists     bool
	Identity   RestoreObjectIdentity
	Partitions []Partition
}

type RestoreObjectController interface {
	Inspect(context.Context, Endpoint, string, string) (RestoreObjectSnapshot, error)
	Create(context.Context, Endpoint, string, string, Table) error
	Drop(context.Context, Endpoint, string, string) error
}

type SQLRestoreObjectController struct {
	client QueryClient
}

func NewSQLRestoreObjectController(client QueryClient) (*SQLRestoreObjectController, error) {
	if client == nil {
		return nil, errors.New("ClickHouse query client is required")
	}
	return &SQLRestoreObjectController{client: client}, nil
}

func newRestoreOwnershipToken() (string, error) {
	value := make([]byte, restoreOwnershipTokenBytes)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate restore ownership token: %w", err)
	}
	return hex.EncodeToString(value), nil
}

func BuildRestoreTableName(runID, table, ownershipToken string) (string, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return "", errors.New("restore run ID is required")
	}
	if !validIdentifier(table) {
		return "", errors.New("restore target table must be a simple identifier")
	}
	if len(ownershipToken) != restoreOwnershipTokenBytes*2 {
		return "", errors.New("restore ownership token must be 128-bit lowercase hex")
	}
	if decoded, err := hex.DecodeString(ownershipToken); err != nil || len(decoded) != restoreOwnershipTokenBytes || ownershipToken != strings.ToLower(ownershipToken) {
		return "", errors.New("restore ownership token must be 128-bit lowercase hex")
	}
	base := table
	if len(base) > 30 {
		base = base[:30]
	}
	sum := sha256.Sum256([]byte("restore|" + runID + "|" + table + "|" + ownershipToken))
	return fmt.Sprintf("sprp_%s_%s", base, hex.EncodeToString(sum[:])[:32]), nil
}

func (controller *SQLRestoreObjectController) Inspect(ctx context.Context, endpoint Endpoint, database, table string) (RestoreObjectSnapshot, error) {
	if !validIdentifier(database) || !validIdentifier(table) {
		return RestoreObjectSnapshot{}, errors.New("restore object identifiers are invalid")
	}
	tableRaw, err := controller.client.Query(ctx, queryForEndpoint(endpoint, database, queryTables(database, []string{table}), FormatJSONEachRow))
	if err != nil {
		return RestoreObjectSnapshot{}, fmt.Errorf("inspect restore table: %w", err)
	}
	tables, err := parseTables(tableRaw)
	if err != nil {
		return RestoreObjectSnapshot{}, err
	}
	if len(tables) == 0 {
		return RestoreObjectSnapshot{}, nil
	}
	if len(tables) != 1 || tables[0].Database != database || tables[0].Name != table {
		return RestoreObjectSnapshot{}, errors.New("restore object inspection returned an unexpected table set")
	}
	columnsRaw, err := controller.client.Query(ctx, queryForEndpoint(endpoint, database, queryColumns(database, []string{table}), FormatJSONEachRow))
	if err != nil {
		return RestoreObjectSnapshot{}, fmt.Errorf("inspect restore columns: %w", err)
	}
	columns, err := parseColumns(columnsRaw)
	if err != nil {
		return RestoreObjectSnapshot{}, err
	}
	partsRaw, err := controller.client.Query(ctx, queryForEndpoint(endpoint, database, queryParts(database, []string{table}), FormatJSONEachRow))
	if err != nil {
		return RestoreObjectSnapshot{}, fmt.Errorf("inspect restore partitions: %w", err)
	}
	parts, err := parseParts(partsRaw)
	if err != nil {
		return RestoreObjectSnapshot{}, err
	}
	tableMetadata := tables[0]
	key := database + "." + table
	tableMetadata.Columns = columns[key]
	tableMetadata.Partitions = parts[key]
	schema, err := tableSchemaFingerprint(tableMetadata)
	if err != nil {
		return RestoreObjectSnapshot{}, err
	}
	return RestoreObjectSnapshot{
		Exists:     true,
		Identity:   RestoreObjectIdentity{Database: database, Table: table, UUID: tableMetadata.UUID, Engine: tableMetadata.Engine, SchemaFingerprint: schema},
		Partitions: append([]Partition(nil), tableMetadata.Partitions...),
	}, nil
}

func (controller *SQLRestoreObjectController) Create(ctx context.Context, endpoint Endpoint, database, restoreTable string, target Table) error {
	if !validIdentifier(database) || !validIdentifier(restoreTable) || target.Database != database || !validIdentifier(target.Name) {
		return errors.New("restore object creation identifiers are invalid")
	}
	if !safeExchangeTableEngine(target.Engine) {
		return fmt.Errorf("restore object creation is blocked for target engine %q", target.Engine)
	}
	columns, err := transferColumns(target)
	if err != nil {
		return err
	}
	if _, err := controller.client.Query(ctx, queryForEndpoint(endpoint, database,
		fmt.Sprintf("CREATE TABLE %s.%s AS %s.%s", quoteIdentifier(database), quoteIdentifier(restoreTable), quoteIdentifier(database), quoteIdentifier(target.Name)), FormatTSVRaw)); err != nil {
		return fmt.Errorf("create run-owned restore table: %w", err)
	}
	columnList := joinIdentifiers(columns)
	query := fmt.Sprintf("INSERT INTO %s.%s (%s) SELECT %s FROM %s.%s", quoteIdentifier(database), quoteIdentifier(restoreTable), columnList, columnList, quoteIdentifier(database), quoteIdentifier(target.Name))
	if _, err := controller.client.Query(ctx, queryForEndpoint(endpoint, database, query, FormatTSVRaw)); err != nil {
		// The durable creating record owns reconciliation. Deleting by name here
		// could remove an object replaced after the failed INSERT response.
		return fmt.Errorf("copy target into run-owned restore table: %w", err)
	}
	return nil
}

func (controller *SQLRestoreObjectController) Drop(ctx context.Context, endpoint Endpoint, database, table string) error {
	if !validIdentifier(database) || !validIdentifier(table) {
		return errors.New("restore object cleanup identifiers are invalid")
	}
	if _, err := controller.client.Query(ctx, queryForEndpoint(endpoint, database,
		fmt.Sprintf("DROP TABLE IF EXISTS %s.%s", quoteIdentifier(database), quoteIdentifier(table)), FormatTSVRaw)); err != nil {
		return fmt.Errorf("drop run-owned restore table: %w", err)
	}
	return nil
}

func tableSchemaFingerprint(table Table) (string, error) {
	type schemaView struct {
		Engine        string   `json:"engine"`
		EngineFull    string   `json:"engine_full"`
		PartitionKey  string   `json:"partition_key"`
		SortingKey    string   `json:"sorting_key"`
		PrimaryKey    string   `json:"primary_key"`
		StoragePolicy string   `json:"storage_policy"`
		Columns       []Column `json:"columns"`
	}
	if !safeExchangeTableEngine(table.Engine) || len(table.Columns) == 0 {
		return "", errors.New("restore schema requires a supported MergeTree table with columns")
	}
	view := schemaView{
		Engine: strings.TrimSpace(table.Engine), EngineFull: strings.TrimSpace(table.EngineFull),
		PartitionKey: normalizeExpression(table.PartitionKey), SortingKey: normalizeExpression(table.SortingKey),
		PrimaryKey: normalizeExpression(table.PrimaryKey), StoragePolicy: strings.TrimSpace(table.StoragePolicy),
		Columns: append([]Column(nil), table.Columns...),
	}
	payload, err := json.Marshal(view)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
