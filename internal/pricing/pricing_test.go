package pricing

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moveeeax/k8s-orphaned-pvc-finder/internal/finder"
)

func writeFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "prices.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func sampleResult() finder.Result {
	return finder.Result{
		Orphans: []finder.Orphan{
			{Name: "a", Namespace: "ns", StorageClass: "gp3", CapacityBytes: 5 * gib},
			{Name: "b", Namespace: "ns", StorageClass: "io2", CapacityBytes: 100 * gib},
			{Name: "c", Namespace: "ns", StorageClass: "", CapacityBytes: 1 * gib},
			{Name: "d", Namespace: "ns", StorageClass: "fancy", CapacityBytes: 3 * gib},
		},
		TotalBytes:     109 * gib,
		ByStorageClass: map[string]int64{"gp3": 5 * gib, "io2": 100 * gib, "<none>": gib, "fancy": 3 * gib},
	}
}

func TestLoadFile(t *testing.T) {
	p, err := LoadFile(writeFile(t, "currency: USD\ndefault: 0.1\nclasses:\n  gp3: 0.08\n  \"<none>\": 0.05\n"))
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if p.Currency != "USD" || p.Classes["gp3"] != 0.08 || p.Default == nil || *p.Default != 0.1 {
		t.Errorf("unexpected prices: %+v", p)
	}
	if v, ok := p.Lookup(""); !ok || v != 0.05 {
		t.Errorf("Lookup(\"\") = %v, %v; want the <none> price", v, ok)
	}
}

func TestLoadFileRejectsTyposAndNegatives(t *testing.T) {
	for name, body := range map[string]string{
		"unknown key": "clases:\n  gp3: 0.08\n",
		"negative":    "classes:\n  gp3: -1\n",
		"not number":  "classes:\n  gp3: cheap\n",
	} {
		if _, err := LoadFile(writeFile(t, body)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestSetClass(t *testing.T) {
	var p Prices
	if err := p.SetClass("gp3=0.08"); err != nil {
		t.Fatalf("SetClass: %v", err)
	}
	if p.Classes["gp3"] != 0.08 {
		t.Errorf("gp3 = %v", p.Classes["gp3"])
	}
	for _, bad := range []string{"gp3", "=0.1", "gp3=", "gp3=-2", "gp3=x"} {
		if err := p.SetClass(bad); err == nil {
			t.Errorf("SetClass(%q) should fail", bad)
		}
	}
}

func TestApplyCostsAndUnpriced(t *testing.T) {
	res := sampleResult()
	Apply(&res, Prices{Currency: "USD", Classes: map[string]float64{"gp3": 0.08, "io2": 0.125, "<none>": 0.10}})

	want := map[string]float64{"a": 0.40, "b": 12.50, "c": 0.10}
	for _, o := range res.Orphans {
		if o.Name == "d" {
			if o.MonthlyCost != nil {
				t.Errorf("unpriced class must not get a cost, got %v", *o.MonthlyCost)
			}
			continue
		}
		if o.MonthlyCost == nil || *o.MonthlyCost != want[o.Name] {
			t.Errorf("%s cost = %v, want %v", o.Name, o.MonthlyCost, want[o.Name])
		}
	}
	if res.TotalMonthlyCost == nil || *res.TotalMonthlyCost != 13.00 {
		t.Errorf("total = %v, want 13.00 (unpriced excluded)", res.TotalMonthlyCost)
	}
	if strings.Join(res.UnpricedStorageClasses, ",") != "fancy" {
		t.Errorf("unpriced = %v, want [fancy]", res.UnpricedStorageClasses)
	}
	if res.MonthlyCostByStorageClass["io2"] != 12.50 {
		t.Errorf("io2 class cost = %v", res.MonthlyCostByStorageClass["io2"])
	}
	if res.Currency != "USD" {
		t.Errorf("currency = %q", res.Currency)
	}
}

func TestApplyDefaultPrice(t *testing.T) {
	res := sampleResult()
	d := 0.10
	Apply(&res, Prices{Default: &d, Classes: map[string]float64{"io2": 0.125}})
	if len(res.UnpricedStorageClasses) != 0 {
		t.Errorf("default price must cover every class, unpriced = %v", res.UnpricedStorageClasses)
	}
	// 12.50 io2 + (5+1+3) GiB * 0.10
	if *res.TotalMonthlyCost != 13.40 {
		t.Errorf("total = %v, want 13.40", *res.TotalMonthlyCost)
	}
}
