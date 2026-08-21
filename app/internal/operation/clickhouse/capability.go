package clickhouse

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"setpoint/internal/executor"
)

type CommitCapability struct {
	DatabaseEngine string `json:"database_engine"`
	TableEngine    string `json:"table_engine"`
	ExchangeTables bool   `json:"exchange_tables"`
	Reason         string `json:"reason,omitempty"`
}

func InspectCommitCapability(ctx context.Context, client QueryClient, endpoint Endpoint, database, table string) (CommitCapability, error) {
	if client == nil {
		return CommitCapability{}, errors.New("ClickHouse query client is required")
	}
	if !validIdentifier(database) || !validIdentifier(table) {
		return CommitCapability{}, errors.New("commit capability identifiers are invalid")
	}
	databaseEngine, err := client.Query(ctx, queryForEndpoint(endpoint, database,
		fmt.Sprintf("SELECT engine FROM system.databases WHERE name = %s", quoteLiteral(database)), FormatTSVRaw))
	if err != nil {
		return CommitCapability{}, fmt.Errorf("discover ClickHouse database engine: %w", err)
	}
	tableEngine, err := client.Query(ctx, queryForEndpoint(endpoint, database,
		fmt.Sprintf("SELECT engine FROM system.tables WHERE database = %s AND name = %s", quoteLiteral(database), quoteLiteral(table)), FormatTSVRaw))
	if err != nil {
		return CommitCapability{}, fmt.Errorf("discover ClickHouse table engine: %w", err)
	}
	capability := CommitCapability{DatabaseEngine: strings.TrimSpace(databaseEngine), TableEngine: strings.TrimSpace(tableEngine)}
	if capability.DatabaseEngine != "Atomic" {
		capability.Reason = "atomic EXCHANGE commit requires an Atomic database in the current implementation"
		return capability, nil
	}
	if !safeExchangeTableEngine(capability.TableEngine) {
		capability.Reason = "current EXCHANGE commit supports only non-replicated MergeTree-family target tables"
		return capability, nil
	}
	probeTable := "__setpoint_exchange_probe__"
	probe := fmt.Sprintf("EXPLAIN SYNTAX EXCHANGE TABLES %s.%s AND %s.%s",
		quoteIdentifier(database), quoteIdentifier(table), quoteIdentifier(database), quoteIdentifier(probeTable))
	output, err := client.Query(ctx, queryForEndpoint(endpoint, database, probe, FormatTSVRaw))
	if err != nil {
		if isUnsupportedExchangeSyntax(err) {
			capability.Reason = "server parser rejected the read-only EXCHANGE TABLES syntax probe (ClickHouse client exit code 62)"
			return capability, nil
		}
		return capability, fmt.Errorf("probe ClickHouse EXCHANGE TABLES syntax: %w", err)
	}
	if !matchesExchangeSyntaxProof(output, database, table, probeTable) {
		capability.Reason = "read-only EXCHANGE syntax probe returned an empty or unexpected normalized statement"
		return capability, nil
	}
	capability.ExchangeTables = true
	return capability, nil
}

func isUnsupportedExchangeSyntax(err error) bool {
	var executionError *executor.Error
	return errors.As(err, &executionError) && executionError.Kind == executor.ErrorExit && executionError.Result.ExitCode == 62
}

func matchesExchangeSyntaxProof(output, database, table, probeTable string) bool {
	canonical := strings.TrimSpace(output)
	canonical = strings.TrimSuffix(canonical, ";")
	canonical = strings.ReplaceAll(canonical, "`", "")
	canonical = strings.Join(strings.Fields(canonical), " ")
	expected := fmt.Sprintf("EXCHANGE TABLES %s.%s AND %s.%s", database, table, database, probeTable)
	return canonical == expected
}

func safeExchangeTableEngine(engine string) bool {
	engine = strings.TrimSpace(engine)
	if engine == "" {
		return false
	}
	lower := strings.ToLower(engine)
	if strings.HasPrefix(lower, "replicated") || lower == "distributed" || lower == "materializedview" {
		return false
	}
	return strings.Contains(lower, "mergetree")
}
