package clickhouse

import (
	"strings"
	"testing"
)

func TestMutationQueryQualifiesFilterAgainstOutputAlias(t *testing.T) {
	query := queryMutations("sp_lab_run", []string{"events"})
	if !strings.Contains(query, "system.mutations.is_done = 0") {
		t.Fatalf("mutation filter must qualify the source column: %s", query)
	}
	if strings.Contains(query, " AND is_done = 0") {
		t.Fatalf("unqualified mutation filter can be replaced by the output alias: %s", query)
	}
}
