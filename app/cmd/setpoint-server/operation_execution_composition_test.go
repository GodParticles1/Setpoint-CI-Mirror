package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"setpoint/internal/operation/clickhouse"
	"setpoint/internal/operation/xrocketreaddress"
)

func TestServerComposesAcceptedProductExecutionCapabilitiesAndRestartResume(t *testing.T) {
	registry, err := newServerOperationRegistry()
	if err != nil {
		t.Fatal(err)
	}
	metadata, ok := registry.Get(xrocketreaddress.OperationID)
	if !ok || metadata.Version != xrocketreaddress.Metadata().Version {
		t.Fatalf("xRocket catalog metadata missing or changed: ok=%v metadata=%#v", ok, metadata)
	}
	normalized, err := registry.NormalizeParameters(xrocketreaddress.OperationID, json.RawMessage(`{"master_target_address":" 198.51.100.10 ","slave_target_address":"198.51.100.11","vip_target_address":"198.51.100.12","external_db_target_address":"203.0.113.20"}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(normalized) != `{"master_target_address":"198.51.100.10","slave_target_address":"198.51.100.11","vip_target_address":"198.51.100.12","external_db_target_address":"203.0.113.20"}` {
		t.Fatalf("normalized parameters=%s", normalized)
	}

	resolver, err := newProductExecutionResolver()
	if err != nil {
		t.Fatal(err)
	}
	xrocketCapability, ok := resolver.Resolve(xrocketreaddress.OperationID)
	if !ok || !xrocketCapability.ApplyAvailable || xrocketCapability.BlockCode != "" {
		t.Fatalf("xRocket product capability must be exposed only with accepted Agent execution composition: ok=%v capability=%#v", ok, xrocketCapability)
	}
	clickhouseCapability, ok := resolver.Resolve(clickhouse.OperationID)
	if !ok || !clickhouseCapability.ApplyAvailable {
		t.Fatalf("ClickHouse migration Apply capability must remain available: ok=%v capability=%#v", ok, clickhouseCapability)
	}

	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	if !strings.Contains(text, "productOperations.ResumeOperationRuns(context.Background())") {
		t.Fatal("Server product execution composition lost durable restart resume")
	}
	if strings.Contains(text, "enable_apply") {
		t.Fatal("Server composition added a global Apply switch")
	}
}
