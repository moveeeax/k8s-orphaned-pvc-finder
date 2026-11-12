package report

import (
	"bytes"
	"encoding/csv"
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
				AccessModes: []string{"ReadWriteOnce"}, Capacity: "100Gi", CapacityBytes: 100 * gib,
				CreationTimestamp: "2026-01-01T00:00:00Z", Reason: finder.ReasonUnreferenced},
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
	for _, want := range []string{"leftover", "data", "io2", "RWO", "100Gi", "10d", "unreferenced", "100.00 GiB"} {
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

// pricedResult is sampleResult plus a finished-only gp3 claim and an
// unpriced one, as pricing.Apply would leave it.
func pricedResult() finder.Result {
	res := sampleResult()
	cost := 12.5
	res.Orphans[0].MonthlyCost = &cost
	res.Orphans = append(res.Orphans, finder.Orphan{
		Name: "report", Namespace: "jobs", Phase: "Bound", StorageClass: "fancy",
		AccessModes: []string{"ReadWriteOnce", "ReadOnlyMany"}, Capacity: "2Gi", CapacityBytes: 2 * gib,
		Reason: finder.ReasonFinishedOnly, LastUsedBy: "report-123",
	})
	res.TotalBytes += 2 * gib
	res.ByStorageClass["fancy"] = 2 * gib
	total := 12.5
	res.Currency = "USD"
	res.TotalMonthlyCost = &total
	res.MonthlyCostByStorageClass = map[string]float64{"io2": 12.5}
	res.UnpricedStorageClasses = []string{"fancy"}
	res.StatefulSetRetained = 2
	return res
}

func TestTableRendersCosts(t *testing.T) {
	var buf bytes.Buffer
	if err := Table(&buf, pricedResult(), time.Date(2026, 1, 11, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("Table: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"COST/MO", "12.50 USD", "RWO,ROX", "finished-only (report-123)",
		"12.50 USD/month (excludes unpriced: fancy)", "12.50 USD/month\n",
		"2 unreferenced PVC(s) skipped",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q\n%s", want, out)
		}
	}
	// The unpriced row and class line show "?", never a zero.
	if strings.Contains(out, "0.00 USD") {
		t.Errorf("unpriced claim rendered as zero:\n%s", out)
	}
	if !strings.Contains(out, "?") {
		t.Errorf("unpriced claim must render as ?\n%s", out)
	}
}

func TestTableWithoutPricesHasNoCostColumn(t *testing.T) {
	var buf bytes.Buffer
	if err := Table(&buf, sampleResult(), time.Now()); err != nil {
		t.Fatalf("Table: %v", err)
	}
	if strings.Contains(buf.String(), "COST/MO") || strings.Contains(buf.String(), "/month") {
		t.Errorf("cost output without prices:\n%s", buf.String())
	}
}

func TestTableEmptyStillReportsRetained(t *testing.T) {
	var buf bytes.Buffer
	if err := Table(&buf, finder.Result{StatefulSetRetained: 1}, time.Now()); err != nil {
		t.Fatalf("Table: %v", err)
	}
	if !strings.Contains(buf.String(), "No orphaned") || !strings.Contains(buf.String(), "1 unreferenced PVC(s) skipped") {
		t.Errorf("unexpected output %q", buf.String())
	}
}

func TestJSONCostFields(t *testing.T) {
	var buf bytes.Buffer
	if err := JSON(&buf, pricedResult()); err != nil {
		t.Fatalf("JSON: %v", err)
	}
	out := buf.String()
	for _, want := range []string{`"monthlyCost": 12.5`, `"totalMonthlyCost": 12.5`, `"currency": "USD"`,
		`"unpricedStorageClasses": [`, `"lastUsedBy": "report-123"`, `"statefulSetRetained": 2`} {
		if !strings.Contains(out, want) {
			t.Errorf("JSON missing %s\n%s", want, out)
		}
	}

	// Without prices the cost keys are absent rather than zero.
	buf.Reset()
	if err := JSON(&buf, sampleResult()); err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if strings.Contains(buf.String(), "onthlyCost") {
		t.Errorf("cost keys present without prices:\n%s", buf.String())
	}
}

func TestCSV(t *testing.T) {
	var buf bytes.Buffer
	if err := CSV(&buf, pricedResult()); err != nil {
		t.Fatalf("CSV: %v", err)
	}
	rows, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatalf("CSV not parseable: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want header + 2", len(rows))
	}
	if strings.Join(rows[0], ",") != strings.Join(csvHeader, ",") {
		t.Errorf("header = %v", rows[0])
	}
	if got := rows[1]; got[1] != "leftover" || got[4] != "ReadWriteOnce" || got[6] != "107374182400" || got[10] != "12.50" || got[11] != "USD" {
		t.Errorf("priced row = %v", got)
	}
	// Unpriced: empty cost, never 0.
	if got := rows[2]; got[8] != "finished-only" || got[9] != "report-123" || got[10] != "" || got[4] != "ReadWriteOnce;ReadOnlyMany" {
		t.Errorf("unpriced row = %v", got)
	}
}
