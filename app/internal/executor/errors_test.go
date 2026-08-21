package executor

import (
	"errors"
	"testing"
)

func TestTrustedResolverClassifiesMissingCommand(t *testing.T) {
	execution, err := NewOSExecutor(0)
	if err != nil {
		t.Fatal(err)
	}

	_, err = execution.resolveExecutable("setpoint-command-that-does-not-exist")
	if !errors.Is(err, ErrCommandNotFound) {
		t.Fatalf("resolveExecutable() error = %v, want ErrCommandNotFound", err)
	}
}
