package clickhouse

import (
	"context"
	"errors"
	"strings"
	"testing"

	"setpoint/internal/executor"
)

type commitCapabilityProbeClient struct {
	probeOutput string
	probeErr    error
	queries     []string
}

func (client *commitCapabilityProbeClient) Query(_ context.Context, request QueryRequest) (string, error) {
	client.queries = append(client.queries, request.Query)
	switch {
	case strings.Contains(request.Query, "FROM system.databases"):
		return "Atomic", nil
	case strings.Contains(request.Query, "FROM system.tables"):
		return "MergeTree", nil
	case strings.HasPrefix(request.Query, "EXPLAIN SYNTAX EXCHANGE TABLES"):
		return client.probeOutput, client.probeErr
	default:
		return "", errors.New("unexpected query")
	}
}

func TestInspectCommitCapabilityRequiresReadOnlyExchangeSyntaxProof(t *testing.T) {
	client := &commitCapabilityProbeClient{probeErr: &executor.Error{Kind: executor.ErrorExit, Result: executor.Result{ExitCode: 62}, Err: errors.New("exit status 62")}}
	capability, err := InspectCommitCapability(context.Background(), client, Endpoint{Host: "target", Port: 9000}, "db", "events")
	if err != nil {
		t.Fatal(err)
	}
	if capability.ExchangeTables || !strings.Contains(capability.Reason, "read-only EXCHANGE TABLES syntax probe") || !strings.Contains(capability.Reason, "exit code 62") {
		t.Fatalf("unsupported EXCHANGE parser accepted: %#v", capability)
	}
	if len(client.queries) != 3 || client.queries[2] != "EXPLAIN SYNTAX EXCHANGE TABLES `db`.`events` AND `db`.`__setpoint_exchange_probe__`" {
		t.Fatalf("unexpected capability queries: %#v", client.queries)
	}
}

func TestInspectCommitCapabilityAcceptsProvenExchangeSyntax(t *testing.T) {
	client := &commitCapabilityProbeClient{probeOutput: "EXCHANGE TABLES `db`.`events` AND `db`.`__setpoint_exchange_probe__`\n"}
	capability, err := InspectCommitCapability(context.Background(), client, Endpoint{Host: "target", Port: 9000}, "db", "events")
	if err != nil || !capability.ExchangeTables || capability.Reason != "" {
		t.Fatalf("capability=%#v err=%v", capability, err)
	}
}

func TestInspectCommitCapabilityRejectsUnprovenSuccessfulProbeOutput(t *testing.T) {
	for _, output := range []string{"", "SELECT 1", "EXCHANGE TABLES db.events AND db.unexpected"} {
		t.Run(output, func(t *testing.T) {
			client := &commitCapabilityProbeClient{probeOutput: output}
			capability, err := InspectCommitCapability(context.Background(), client, Endpoint{Host: "target", Port: 9000}, "db", "events")
			if err != nil {
				t.Fatal(err)
			}
			if capability.ExchangeTables || !strings.Contains(capability.Reason, "empty or unexpected") {
				t.Fatalf("unproven probe output accepted: output=%q capability=%#v", output, capability)
			}
		})
	}
}

func TestInspectCommitCapabilityPropagatesNonParserProbeFailures(t *testing.T) {
	tests := map[string]error{
		"canceled":   context.Canceled,
		"timeout":    context.DeadlineExceeded,
		"start":      &executor.Error{Kind: executor.ErrorStart, Result: executor.Result{ExitCode: -1}, Err: errors.New("client unavailable")},
		"other exit": &executor.Error{Kind: executor.ErrorExit, Result: executor.Result{ExitCode: 1}, Err: errors.New("exit status 1")},
	}
	for name, probeErr := range tests {
		t.Run(name, func(t *testing.T) {
			client := &commitCapabilityProbeClient{probeErr: probeErr}
			capability, err := InspectCommitCapability(context.Background(), client, Endpoint{Host: "target", Port: 9000}, "db", "events")
			if err == nil || capability.ExchangeTables {
				t.Fatalf("probe failure was converted to a capability finding: capability=%#v err=%v", capability, err)
			}
			if !errors.Is(err, probeErr) {
				t.Fatalf("probe failure was not preserved: got %v want %v", err, probeErr)
			}
		})
	}
}
