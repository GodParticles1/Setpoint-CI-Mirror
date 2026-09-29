package main

import (
	"os"
	"strings"
	"testing"
)

func TestAgentComposesOperationExecutionAdaptersAndFailClosedXRocketDiscovery(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, required := range []string{
		"agent.NewStaticOperationExecutionAdapter(sysctlrepair.ID",
		"agent.NewClickHouseOperationExecutionAdapter(",
		"agent.NewOperationExecutionResolver(sysctlAdapter, clickHouseAdapter)",
		"xrocketreaddress.NewDefinition(commandExecutor)",
		"operationRegistry.Register(xrocketReaddressDefinition)",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("Agent product execution composition is missing %q", required)
		}
	}
	for _, forbidden := range []string{"sysctlrepair.NewDefinitionFactory", "enable_apply", "xrocketReaddressAdapter"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("Agent composition retains forbidden wiring %q", forbidden)
		}
	}
}
