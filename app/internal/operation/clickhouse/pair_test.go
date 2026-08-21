package clickhouse

import (
	"encoding/json"
	"testing"
)

func TestParsePairParametersRejectsUnknownFields(t *testing.T) {
	for _, raw := range []string{
		`{"source":{"host":"source","passwrod":"value"},"target":{"host":"target"},"database":"db","tables":["events"]}`,
		`{"source":{"host":"source"},"target":{"host":"target"},"database":"db","tables":["events"],"strategy":"native"}`,
	} {
		if _, err := ParsePairParameters(json.RawMessage(raw)); err == nil {
			t.Fatalf("unknown field accepted: %s", raw)
		}
	}
}
