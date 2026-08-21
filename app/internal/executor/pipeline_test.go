package executor

import (
	"context"
	"runtime"
	"strings"
	"testing"
)

func TestOSExecutorStreamsPipelineWithoutShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("trusted command fixtures are Linux-specific")
	}
	executor, err := NewOSExecutor(64 << 10)
	if err != nil { t.Fatal(err) }
	result, err := executor.ExecutePipeline(context.Background(), Command{Name: "printf", Args: []string{"hello"}}, Command{Name: "wc", Args: []string{"-c"}})
	if err != nil { t.Fatal(err) }
	if result.BytesTransferred != 5 { t.Fatalf("bytes=%d", result.BytesTransferred) }
	if strings.TrimSpace(result.Target.Stdout) != "5" { t.Fatalf("stdout=%q", result.Target.Stdout) }
}
