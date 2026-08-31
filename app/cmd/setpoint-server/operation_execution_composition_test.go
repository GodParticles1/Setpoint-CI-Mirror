package main

import (
	"os"
	"strings"
	"testing"
)

func TestServerComposesCapabilityScopedProductExecutionAndRestartResume(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, required := range []string{
		"app.NewProductExecutionResolver(",
		"OperationID: sysctlrepair.ID",
		"OperationID: clickhouse.OperationID",
		"productOperations.ResumeOperationRuns(context.Background())",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("Server product execution composition is missing %q", required)
		}
	}
	if strings.Contains(text, "enable_apply") {
		t.Fatal("Server composition added a global Apply switch")
	}
}
