package finder

import (
	"os"
	"path/filepath"
	"testing"
)

const gib = 1024 * 1024 * 1024

// loadFixture parses the committed offline dump so the whole pipeline is
// exercised with no cluster.
func loadFixture(t *testing.T) ([]PVC, []Pod) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "dump.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	pvcs, pods, err := ParseDump(data)
	if err != nil {
		t.Fatalf("parse dump: %v", err)
	}
	return pvcs, pods
}

func TestClassifyOrphans(t *testing.T) {
	pvcs, pods := loadFixture(t)
	res := Classify(pvcs, pods)

	// A PVC mounted by a Pod (shop/data-web-0) must never be reported.
	for _, o := range res.Orphans {
		if o.Namespace == "shop" && o.Name == "data-web-0" {
			t.Fatalf("referenced PVC shop/data-web-0 was wrongly reported orphaned")
		}
	}

	// Exactly the three unreferenced PVCs, sorted by namespace then name.
	wantOrder := []string{"data/leftover-batch", "data/no-class-vol", "shop/data-cache-0"}
	if len(res.Orphans) != len(wantOrder) {
		t.Fatalf("got %d orphans, want %d: %+v", len(res.Orphans), len(wantOrder), res.Orphans)
	}
	for i, want := range wantOrder {
		got := res.Orphans[i].Namespace + "/" + res.Orphans[i].Name
		if got != want {
			t.Errorf("orphan[%d] = %s, want %s", i, got, want)
		}
	}
}

func TestClassifyCapacityAndBreakdown(t *testing.T) {
	pvcs, pods := loadFixture(t)
	res := Classify(pvcs, pods)

	wantTotal := int64(106 * gib) // 100Gi + 1Gi + 5Gi
	if res.TotalBytes != wantTotal {
		t.Errorf("TotalBytes = %d, want %d", res.TotalBytes, wantTotal)
	}

	// Per-storage-class breakdown must sum to the total.
	var sum int64
	for _, b := range res.ByStorageClass {
		sum += b
	}
	if sum != res.TotalBytes {
		t.Errorf("breakdown sum = %d, want %d", sum, res.TotalBytes)
	}

	if got := res.ByStorageClass["io2"]; got != 100*gib {
		t.Errorf("io2 = %d, want %d", got, int64(100*gib))
	}
	if got := res.ByStorageClass["gp3"]; got != 5*gib {
		t.Errorf("gp3 = %d, want %d", got, int64(5*gib))
	}
	// A PVC with no storage class is bucketed under "<none>".
	if got := res.ByStorageClass[noStorageClass]; got != 1*gib {
		t.Errorf("%s = %d, want %d", noStorageClass, got, int64(1*gib))
	}
}

func TestClassifyNoOrphans(t *testing.T) {
	pvcs := []PVC{{Name: "vol", Namespace: "ns", Capacity: "1Gi"}}
	pods := []Pod{{Name: "p", Namespace: "ns", ClaimRefs: []string{"vol"}}}

	res := Classify(pvcs, pods)
	if len(res.Orphans) != 0 {
		t.Fatalf("expected no orphans, got %+v", res.Orphans)
	}
	if res.TotalBytes != 0 {
		t.Errorf("TotalBytes = %d, want 0", res.TotalBytes)
	}
}

func TestClassifyIsNamespaceScoped(t *testing.T) {
	// A same-named claim referenced only in another namespace must NOT rescue
	// the orphan in this namespace.
	pvcs := []PVC{{Name: "shared", Namespace: "a", Capacity: "2Gi"}}
	pods := []Pod{{Name: "p", Namespace: "b", ClaimRefs: []string{"shared"}}}

	res := Classify(pvcs, pods)
	if len(res.Orphans) != 1 {
		t.Fatalf("expected 1 orphan, got %+v", res.Orphans)
	}
	if res.Orphans[0].Namespace != "a" {
		t.Errorf("orphan namespace = %s, want a", res.Orphans[0].Namespace)
	}
}

func TestParseDumpExtractsClaimRefs(t *testing.T) {
	_, pods := loadFixture(t)
	byName := map[string][]string{}
	for _, p := range pods {
		byName[p.Namespace+"/"+p.Name] = p.ClaimRefs
	}
	if refs := byName["shop/web-0"]; len(refs) != 1 || refs[0] != "data-web-0" {
		t.Errorf("web-0 claimRefs = %v, want [data-web-0]", refs)
	}
	// A Pod with only emptyDir/configMap volumes references no PVC.
	if refs := byName["data/worker-0"]; len(refs) != 0 {
		t.Errorf("worker-0 claimRefs = %v, want empty", refs)
	}
}
