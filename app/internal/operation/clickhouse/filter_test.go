package clickhouse

import (
	"strings"
	"testing"
)

func TestTimeRangeFilterIsHalfOpenAndValidated(t *testing.T) {
	filter, err := BuildTimeRangeFilter("event_time", "2026-08-01T00:00:00+08:00", "2026-08-02T00:00:00+08:00")
	if err != nil { t.Fatal(err) }
	where, err := filter.whereClause()
	if err != nil { t.Fatal(err) }
	if !strings.Contains(where, ">=") || !strings.Contains(where, " < ") { t.Fatalf("where=%q", where) }
	if _, err := BuildTimeRangeFilter("event_time", "2026-08-02T00:00:00+08:00", "2026-08-01T00:00:00+08:00"); err == nil { t.Fatal("reverse range accepted") }
}
