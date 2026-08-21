package clickhouse

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"setpoint/internal/executor"
)

type NativeTransferRequest struct {
	Source      Endpoint
	Target      Endpoint
	Chunk       TransferChunk
	SourceTable Table
}

type NativeTransferResult struct {
	BytesTransferred int64 `json:"bytes_transferred"`
	SourceExitCode   int   `json:"source_exit_code"`
	TargetExitCode   int   `json:"target_exit_code"`
}

type NativeTransport interface {
	Transfer(context.Context, NativeTransferRequest) (NativeTransferResult, error)
}

type PipelineNativeTransport struct {
	pipeline      executor.PipelineExecutor
	sourceCommand ClientCommand
	targetCommand ClientCommand
}

func NewPipelineNativeTransport(pipeline executor.PipelineExecutor) (*PipelineNativeTransport, error) {
	return NewPipelineNativeTransportWithCommands(pipeline, DefaultClientCommand(), DefaultClientCommand())
}

func NewPipelineNativeTransportWithCommands(pipeline executor.PipelineExecutor, sourceCommand, targetCommand ClientCommand) (*PipelineNativeTransport, error) {
	if pipeline == nil {
		return nil, errors.New("pipeline executor is required")
	}
	if err := sourceCommand.Validate(); err != nil {
		return nil, fmt.Errorf("source ClickHouse client: %w", err)
	}
	if err := targetCommand.Validate(); err != nil {
		return nil, fmt.Errorf("target ClickHouse client: %w", err)
	}
	return &PipelineNativeTransport{pipeline: pipeline, sourceCommand: sourceCommand, targetCommand: targetCommand}, nil
}

func (transport *PipelineNativeTransport) Transfer(ctx context.Context, request NativeTransferRequest) (NativeTransferResult, error) {
	if err := ValidateTransferChunk(request.Chunk); err != nil {
		return NativeTransferResult{}, err
	}
	columns, err := transferColumns(request.SourceTable)
	if err != nil {
		return NativeTransferResult{}, err
	}
	where, err := transferWhereClause(request.Chunk)
	if err != nil {
		return NativeTransferResult{}, err
	}
	columnList := joinIdentifiers(columns)
	sourceQuery := fmt.Sprintf("SELECT %s FROM %s.%s%s FORMAT Native", columnList, quoteIdentifier(request.Chunk.SourceDatabase), quoteIdentifier(request.Chunk.SourceTable), where)
	targetQuery := fmt.Sprintf("INSERT INTO %s.%s (%s) FORMAT Native", quoteIdentifier(request.Chunk.TargetDatabase), quoteIdentifier(request.Chunk.StagingTable), columnList)

	sourceCommand, err := transport.sourceCommand.Build(clickHouseCommandArgs(request.Source, request.Chunk.SourceDatabase, sourceQuery)...)
	if err != nil {
		return NativeTransferResult{}, err
	}
	targetCommand, err := transport.targetCommand.Build(clickHouseCommandArgs(request.Target, request.Chunk.TargetDatabase, targetQuery)...)
	if err != nil {
		return NativeTransferResult{}, err
	}
	pipelineResult, err := transport.pipeline.ExecutePipeline(ctx, sourceCommand, targetCommand)
	result := NativeTransferResult{BytesTransferred: pipelineResult.BytesTransferred, SourceExitCode: pipelineResult.Source.ExitCode, TargetExitCode: pipelineResult.Target.ExitCode}
	if err != nil {
		return result, fmt.Errorf("native ClickHouse transfer: %w", err)
	}
	return result, nil
}

func transferColumns(table Table) ([]string, error) {
	columns := make([]string, 0, len(table.Columns))
	for _, column := range table.Columns {
		if !validIdentifier(column.Name) {
			return nil, fmt.Errorf("invalid ClickHouse column %q", column.Name)
		}
		kind := strings.ToUpper(strings.TrimSpace(column.DefaultKind))
		if kind == "ALIAS" || kind == "MATERIALIZED" {
			continue
		}
		columns = append(columns, column.Name)
	}
	if len(columns) == 0 {
		return nil, errors.New("no insertable ClickHouse columns were discovered")
	}
	return columns, nil
}

func joinIdentifiers(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, quoteIdentifier(value))
	}
	return strings.Join(quoted, ",")
}

func clickHouseCommandArgs(endpoint Endpoint, database, query string) []string {
	args := []string{"--host", endpoint.Host, "--port", fmt.Sprintf("%d", endpoint.Port), "--database", database, "--query", query}
	if endpoint.User != "" {
		args = append(args, "--user", endpoint.User)
	}
	if endpoint.Secure {
		args = append(args, "--secure")
	}
	return args
}
