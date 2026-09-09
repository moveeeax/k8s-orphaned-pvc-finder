package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/moveeeax/k8s-orphaned-pvc-finder/internal/finder"
)

func sampleResult() finder.Result {
	return finder.Result{
		Orphans: []finder.Orphan{
			{Name: "leftover", Namespace: "data", Phase: "Bound", StorageClass: "io2",
				Capacity: "100Gi", CapacityBytes: 100 * gib, CreationTimestamp: "2026-01-01T00:00:00Z"},
		},
		TotalBytes:     100 * gib,
		ByStorageClass: map[string]int64{"io2": 100 * gib},
	}
}

func TestTableRendersOrphan(t *testing.T) {
	var buf bytes.Buffer
	now := time.Date(2026, 1, 11, 0, 0, 0, 0, time.UTC)
	if err := Table(&buf, sampleResult(), now); err != nil {
		t.Fatalf("Table: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"leftover", "data", "io2", "100Gi", "10d", "100.00 GiB"} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q\n%s", want, out)
		}
	}
}

func TestTableEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := Table(&buf, finder.Result{}, time.Now()); err != nil {
		t.Fatalf("Table: %v", err)
	}
	if !strings.Contains(buf.String(), "No orphaned") {
		t.Errorf("expected empty message, got %q", buf.String())
	}
}

func TestJSONIsStableAndValid(t *testing.T) {
	var a, b bytes.Buffer
	if err := JSON(&a, sampleResult()); err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if err := JSON(&b, sampleResult()); err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if a.String() != b.String() {
		t.Errorf("JSON output not stable:\n%s\n---\n%s", a.String(), b.String())
	}

	var decoded finder.Result
	if err := json.Unmarshal(a.Bytes(), &decoded); err != nil {
		t.Fatalf("JSON not valid: %v", err)
	}
	if len(decoded.Orphans) != 1 || decoded.TotalBytes != 100*gib {
		t.Errorf("round-trip mismatch: %+v", decoded)
	}
}

func TestJSONEmptyIsNonNull(t *testing.T) {
	var buf bytes.Buffer
	if err := JSON(&buf, finder.Result{}); err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if !strings.Contains(buf.String(), `"orphans": []`) {
		t.Errorf("empty orphans should serialize as [], got %s", buf.String())
	}
}

func TestGiB(t *testing.T) {
	if got := GiB(0); got != "0.00 GiB" {
		t.Errorf("GiB(0) = %s", got)
	}
	if got := GiB(gib); got != "1.00 GiB" {
		t.Errorf("GiB(1 gib) = %s", got)
	}
}
