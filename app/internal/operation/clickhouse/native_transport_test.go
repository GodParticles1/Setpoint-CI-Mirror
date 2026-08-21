package clickhouse

import (
	"context"
	"strings"
	"testing"

	"setpoint/internal/executor"
)

type recordingPipeline struct { source executor.Command; target executor.Command }
func (pipeline *recordingPipeline) ExecutePipeline(_ context.Context, source, target executor.Command) (executor.PipelineResult, error) { pipeline.source, pipeline.target = source, target; return executor.PipelineResult{Source: executor.Result{ExitCode: 0}, Target: executor.Result{ExitCode: 0}, BytesTransferred: 55}, nil }

func TestNativeTransportBuildsShellFreeExplicitColumnPipeline(t *testing.T) {
	pipeline := &recordingPipeline{}
	transport, err := NewPipelineNativeTransport(pipeline)
	if err != nil { t.Fatal(err) }
	filter, err := BuildTimeRangeFilter("ts", "2026-08-01T00:00:00+08:00", "2026-08-02T00:00:00+08:00")
	if err != nil { t.Fatal(err) }
	chunk := TransferChunk{RunID: "run-1", Strategy: StrategyNativeStream, SourceDatabase: "db", SourceTable: "events", TargetDatabase: "db", TargetTable: "events", StagingTable: "spmig_events_123", Filter: filter, Sequence: 1}
	table := Table{Name: "events", Columns: []Column{{Name: "id"}, {Name: "computed", DefaultKind: "ALIAS"}, {Name: "ts"}}}
	result, err := transport.Transfer(context.Background(), NativeTransferRequest{Source: Endpoint{Host: "src", Port: 9000}, Target: Endpoint{Host: "dst", Port: 9000}, Chunk: chunk, SourceTable: table})
	if err != nil { t.Fatal(err) }
	if result.BytesTransferred != 55 { t.Fatalf("bytes=%d", result.BytesTransferred) }
	sourceArgs := strings.Join(pipeline.source.Args, " ")
	targetArgs := strings.Join(pipeline.target.Args, " ")
	if strings.Contains(sourceArgs, "computed") || strings.Contains(targetArgs, "computed") { t.Fatalf("ALIAS column leaked into transfer: %s | %s", sourceArgs, targetArgs) }
	if !strings.Contains(sourceArgs, "FORMAT Native") || !strings.Contains(targetArgs, "FORMAT Native") { t.Fatalf("native format missing: %s | %s", sourceArgs, targetArgs) }
	if strings.Contains(sourceArgs, "--password") || strings.Contains(targetArgs, "--password") { t.Fatal("password argument must never be generated") }
}
