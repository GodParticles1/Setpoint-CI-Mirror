package operation

import (
	"reflect"
	"testing"
	"time"
)

func TestNormalizeLockRequest(t *testing.T) {
	request, err := NormalizeLockRequest(LockRequest{OwnerID: " run-1 ", TTL: time.Minute, Resources: []LockResource{{Key: "b"}, {Key: "a"}, {Key: "a"}}})
	if err != nil {
		t.Fatal(err)
	}
	want := []LockResource{{Key: "a"}, {Key: "b"}}
	if !reflect.DeepEqual(request.Resources, want) {
		t.Fatalf("resources=%v want=%v", request.Resources, want)
	}
}

func TestResourceLockKey(t *testing.T) {
	key, err := ResourceLockKey(Target{Kind: TargetDataObject, SiteID: "site-1", Component: "clickhouse", Resource: "db.table/202608"})
	if err != nil {
		t.Fatal(err)
	}
	if key != "data_object|site-1||clickhouse|db.table/202608" {
		t.Fatalf("unexpected key %q", key)
	}
}
