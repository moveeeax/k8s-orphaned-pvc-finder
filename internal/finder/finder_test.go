package finder

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const gib = 1024 * 1024 * 1024

// loadFixture parses the committed offline dump so the whole pipeline is
// exercised with no cluster.
func loadFixture(t *testing.T) ([]PVC, []Pod) {
	t.Helper()
	inv := loadInventory(t, "dump.json")
	return inv.PVCs, inv.Pods
}

func loadInventory(t *testing.T, name string) Inventory {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	inv, err := ParseDump(data)
	if err != nil {
		t.Fatalf("parse dump: %v", err)
	}
	return inv
}

// classify runs Classify with default options over plain PVC and Pod lists.
func classify(pvcs []PVC, pods []Pod) Result {
	return Classify(Inventory{PVCs: pvcs, Pods: pods}, Options{})
}

// byKey indexes orphans by namespace/name.
func byKey(res Result) map[string]Orphan {
	m := map[string]Orphan{}
	for _, o := range res.Orphans {
		m[o.Namespace+"/"+o.Name] = o
	}
	return m
}

func TestClassifyOrphans(t *testing.T) {
	pvcs, pods := loadFixture(t)
	res := classify(pvcs, pods)

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
	res := classify(pvcs, pods)

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
	if got := res.ByStorageClass[NoStorageClass]; got != 1*gib {
		t.Errorf("%s = %d, want %d", NoStorageClass, got, int64(1*gib))
	}
}

func TestClassifyNoOrphans(t *testing.T) {
	pvcs := []PVC{{Name: "vol", Namespace: "ns", Capacity: "1Gi"}}
	pods := []Pod{{Name: "p", Namespace: "ns", ClaimRefs: []string{"vol"}}}

	res := classify(pvcs, pods)
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

	res := classify(pvcs, pods)
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

func TestClassifyIgnoresFinishedPods(t *testing.T) {
	res := Classify(loadInventory(t, "lifecycle.yaml"), Options{})
	got := byKey(res)

	o, ok := got["jobs/nightly-report"]
	if !ok {
		t.Fatalf("claim referenced only by finished Pods must be orphaned, got %+v", res.Orphans)
	}
	if o.Reason != ReasonFinishedOnly {
		t.Errorf("reason = %s, want %s", o.Reason, ReasonFinishedOnly)
	}
	// The newest finished Pod wins, whatever its terminal phase.
	if o.LastUsedBy != "nightly-report-29001440-bbbbb" {
		t.Errorf("lastUsedBy = %q, want the newest finished Pod", o.LastUsedBy)
	}

	// Pending and Running Pods keep their claims in use.
	for _, k := range []string{"jobs/queue-buffer", "db/data-db-0"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s is referenced by an active Pod but was reported", k)
		}
	}
}

func TestClassifyCountAllRestoresOldBehaviour(t *testing.T) {
	res := Classify(loadInventory(t, "lifecycle.yaml"), Options{CountPods: CountAll})
	if _, ok := byKey(res)["jobs/nightly-report"]; ok {
		t.Errorf("with CountAll a finished Pod must keep its claim referenced")
	}
}

func TestClassifyEmptyPodPhaseCountsAsActive(t *testing.T) {
	pvcs := []PVC{{Name: "vol", Namespace: "ns"}}
	pods := []Pod{{Name: "p", Namespace: "ns", ClaimRefs: []string{"vol"}}}
	if res := classify(pvcs, pods); len(res.Orphans) != 0 {
		t.Errorf("a Pod with no phase must count as a reference, got %+v", res.Orphans)
	}
}

func TestClassifySkipsStatefulSetClaims(t *testing.T) {
	res := Classify(loadInventory(t, "lifecycle.yaml"), Options{})
	got := byKey(res)

	if _, ok := got["db/data-db-2"]; ok {
		t.Errorf("claim of a scaled-down replica of a live StatefulSet must be skipped")
	}
	if res.StatefulSetRetained != 1 {
		t.Errorf("StatefulSetRetained = %d, want 1", res.StatefulSetRetained)
	}
	// Matching the prefix without a numeric ordinal is not ownership.
	if _, ok := got["db/data-db-old"]; !ok {
		t.Errorf("db/data-db-old does not follow <template>-<sts>-<ordinal> and must be reported")
	}
}

func TestStatefulSetOwnershipIsNamespaceScoped(t *testing.T) {
	inv := Inventory{
		PVCs:         []PVC{{Name: "data-db-1", Namespace: "a"}},
		StatefulSets: []StatefulSet{{Name: "db", Namespace: "b", ClaimTemplates: []string{"data"}}},
	}
	res := Classify(inv, Options{})
	if len(res.Orphans) != 1 || res.StatefulSetRetained != 0 {
		t.Errorf("a StatefulSet in another namespace must not own the claim: %+v", res)
	}
}

func TestClassifyReasonsAndAccessModes(t *testing.T) {
	got := byKey(Classify(loadInventory(t, "lifecycle.yaml"), Options{}))

	if o := got["db/shared-files"]; o.Reason != ReasonNeverBound {
		t.Errorf("pending claim reason = %q, want %s", o.Reason, ReasonNeverBound)
	}
	// An unbound claim has no status.accessModes; the requested ones are used.
	if o := got["db/shared-files"]; len(o.AccessModes) != 1 || o.AccessModes[0] != "ReadWriteMany" {
		t.Errorf("shared-files accessModes = %v, want [ReadWriteMany]", o.AccessModes)
	}
	if o := got["db/data-db-old"]; o.Reason != ReasonUnreferenced || o.LastUsedBy != "" {
		t.Errorf("data-db-old = %+v, want reason unreferenced and no lastUsedBy", o)
	}
}

func TestClassifyMinAge(t *testing.T) {
	inv := Inventory{PVCs: []PVC{
		{Name: "old", Namespace: "ns", CreationTimestamp: "2026-01-01T00:00:00Z", Capacity: "1Gi"},
		{Name: "young", Namespace: "ns", CreationTimestamp: "2026-03-25T00:00:00Z", Capacity: "1Gi"},
		{Name: "unknown", Namespace: "ns", Capacity: "1Gi"},
	}}
	now := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	res := Classify(inv, Options{MinAge: 30 * 24 * time.Hour, Now: now})
	got := byKey(res)

	if _, ok := got["ns/young"]; ok {
		t.Errorf("claim younger than MinAge must be dropped")
	}
	// An unknown creation time is never a reason to hide a claim.
	for _, k := range []string{"ns/old", "ns/unknown"} {
		if _, ok := got[k]; !ok {
			t.Errorf("%s must be kept", k)
		}
	}
	if res.TotalBytes != 2*gib {
		t.Errorf("TotalBytes = %d, want only kept claims (%d)", res.TotalBytes, int64(2*gib))
	}
}

func TestParseAge(t *testing.T) {
	cases := map[string]time.Duration{
		"":    0,
		"30d": 30 * 24 * time.Hour,
		"12h": 12 * time.Hour,
		"90m": 90 * time.Minute,
	}
	for in, want := range cases {
		got, err := ParseAge(in)
		if err != nil || got != want {
			t.Errorf("ParseAge(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"30", "d", "-1d", "1.5d", "-5h", "soon"} {
		if _, err := ParseAge(bad); err == nil {
			t.Errorf("ParseAge(%q) should fail", bad)
		}
	}
}

func TestFilterNamespaces(t *testing.T) {
	inv := loadInventory(t, "lifecycle.yaml")
	f := inv.FilterNamespaces([]string{"jobs"})
	if len(f.PVCs) != 2 || len(f.StatefulSets) != 0 {
		t.Errorf("filter kept %d PVCs and %d StatefulSets, want 2 and 0", len(f.PVCs), len(f.StatefulSets))
	}
	for _, p := range f.Pods {
		if p.Namespace != "jobs" {
			t.Errorf("pod %s/%s leaked through the filter", p.Namespace, p.Name)
		}
	}
	if all := inv.FilterNamespaces(nil); len(all.PVCs) != len(inv.PVCs) {
		t.Errorf("empty filter must keep everything")
	}
}

func TestParseDumpReadsPhaseAndStatefulSets(t *testing.T) {
	inv := loadInventory(t, "lifecycle.yaml")
	phases := map[string]string{}
	for _, p := range inv.Pods {
		phases[p.Name] = p.Phase
	}
	if phases["db-0"] != "Running" || phases["nightly-report-29000000-aaaaa"] != "Succeeded" {
		t.Errorf("pod phases not parsed: %v", phases)
	}
	if len(inv.StatefulSets) != 1 || inv.StatefulSets[0].ClaimTemplates[0] != "data" {
		t.Errorf("statefulsets = %+v, want db with template data", inv.StatefulSets)
	}
}
