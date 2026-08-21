package clickhouse

import (
	"context"
	"errors"
	"testing"
)

type stagingQueryFunc func(context.Context, QueryRequest) (string, error)
func (fn stagingQueryFunc) Query(ctx context.Context, request QueryRequest) (string, error) { return fn(ctx, request) }

func TestBuildStagingTableNameIsStableAndIdentifierSafe(t *testing.T) {
	first, err := BuildStagingTableName("run-123", "alarm")
	if err != nil { t.Fatal(err) }
	second, err := BuildStagingTableName("run-123", "alarm")
	if err != nil { t.Fatal(err) }
	if first != second { t.Fatalf("staging name is not stable: %q != %q", first, second) }
	if !validIdentifier(first) { t.Fatalf("staging name is not a simple identifier: %q", first) }
}

func TestSQLStagingControllerExistsIsFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name string
		output string
		queryErr error
		want bool
		wantErr bool
	}{
		{name: "absent", output: "0", want: false},
		{name: "present", output: "1", want: true},
		{name: "unexpected_count", output: "2", wantErr: true},
		{name: "query_error", queryErr: errors.New("offline"), wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := stagingQueryFunc(func(_ context.Context, request QueryRequest) (string, error) {
				if request.Format != FormatTSVRaw { t.Fatalf("format=%s", request.Format) }
				if request.Database != "db" { t.Fatalf("database=%s", request.Database) }
				return tc.output, tc.queryErr
			})
			controller, err := NewSQLStagingController(client)
			if err != nil { t.Fatal(err) }
			got, err := controller.Exists(context.Background(), Endpoint{Host: "target", Port: 9000}, "db", "spmig_events_deadbeef0000")
			if tc.wantErr {
				if err == nil { t.Fatalf("got=%v; expected error", got) }
				return
			}
			if err != nil { t.Fatal(err) }
			if got != tc.want { t.Fatalf("got=%v want=%v", got, tc.want) }
		})
	}
}
