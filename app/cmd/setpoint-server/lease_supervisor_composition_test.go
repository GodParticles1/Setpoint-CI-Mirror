package main

import (
	"os"
	"strings"
	"testing"
)

func TestServerComposesSingleOperationLeaseSupervisor(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	if count := strings.Count(text, "operation.NewLeaseSupervisor("); count != 1 {
		t.Fatalf("expected one Server lease supervisor construction, got %d", count)
	}
	for _, required := range []string{
		"defer leaseSupervisor.Close()",
		"app.NewServiceWithOperationLeaseAuthority(store, store, registry, config.OfflineAfter, leaseSupervisor)",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("Server lease supervisor composition is missing %q", required)
		}
	}
	for _, forbidden := range []string{"leaseSupervisor.Acquire(", "leaseSupervisor.Resume("} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("Server startup must not invoke %q", forbidden)
		}
	}
}
