// Package finder holds the pure orphaned-PVC classification logic.
//
// It is deliberately free of any Kubernetes client dependency: it operates on
// small domain structs so the core can be unit-tested offline against fixtures
// with no API server. The live-cluster and dump loaders both convert into these
// types and hand them to Classify.
package finder

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
)

// PVC is the minimal view of a PersistentVolumeClaim the finder needs.
type PVC struct {
	Name              string   `json:"name"`
	Namespace         string   `json:"namespace"`
	Phase             string   `json:"phase"`
	StorageClass      string   `json:"storageClass"`
	AccessModes       []string `json:"accessModes"`
	Capacity          string   `json:"capacity"`
	CreationTimestamp string   `json:"creationTimestamp"`
}

// Pod is the minimal view of a Pod: its phase and the PVC names it mounts.
type Pod struct {
	Name              string   `json:"name"`
	Namespace         string   `json:"namespace"`
	Phase             string   `json:"phase"`
	CreationTimestamp string   `json:"creationTimestamp"`
	ClaimRefs         []string `json:"claimRefs"`
}

// StatefulSet is the minimal view of a StatefulSet: the names of its
// volumeClaimTemplates. The controller names each replica's claim
// "<template>-<statefulset>-<ordinal>".
type StatefulSet struct {
	Name           string   `json:"name"`
	Namespace      string   `json:"namespace"`
	ClaimTemplates []string `json:"claimTemplates"`
}

// Inventory is everything a scan looks at.
type Inventory struct {
	PVCs         []PVC
	Pods         []Pod
	StatefulSets []StatefulSet
}

// Orphan reasons.
const (
	// ReasonUnreferenced: no Pod lists the claim in spec.volumes.
	ReasonUnreferenced = "unreferenced"
	// ReasonFinishedOnly: only Succeeded/Failed Pods list the claim.
	ReasonFinishedOnly = "finished-only"
	// ReasonNeverBound: the claim is Pending, so no volume was ever bound and
	// nothing has mounted it.
	ReasonNeverBound = "never-bound"
)

// Orphan is a PVC that no active Pod references.
type Orphan struct {
	Name              string   `json:"name"`
	Namespace         string   `json:"namespace"`
	Phase             string   `json:"phase"`
	StorageClass      string   `json:"storageClass"`
	AccessModes       []string `json:"accessModes"`
	Capacity          string   `json:"capacity"`
	CapacityBytes     int64    `json:"capacityBytes"`
	CreationTimestamp string   `json:"creationTimestamp"`
	Reason            string   `json:"reason"`
	// LastUsedBy names the newest finished Pod that still lists the claim; set
	// only when Reason is ReasonFinishedOnly.
	LastUsedBy string `json:"lastUsedBy,omitempty"`
	// MonthlyCost is set by pricing.Apply when a price is known for the
	// storage class.
	MonthlyCost *float64 `json:"monthlyCost,omitempty"`
}

// Result is the outcome of a scan.
type Result struct {
	Orphans        []Orphan         `json:"orphans"`
	TotalBytes     int64            `json:"totalBytes"`
	ByStorageClass map[string]int64 `json:"byStorageClass"`
	// StatefulSetRetained counts unreferenced claims skipped because a live
	// StatefulSet still owns them through a volumeClaimTemplate.
	StatefulSetRetained int `json:"statefulSetRetained"`

	// Cost fields, filled by pricing.Apply only when prices were supplied.
	Currency                  string             `json:"currency,omitempty"`
	TotalMonthlyCost          *float64           `json:"totalMonthlyCost,omitempty"`
	MonthlyCostByStorageClass map[string]float64 `json:"monthlyCostByStorageClass,omitempty"`
	UnpricedStorageClasses    []string           `json:"unpricedStorageClasses,omitempty"`
}

// PodCount selects which Pods count as references to a claim.
type PodCount string

const (
	// CountRunning counts only Pods that are not finished: Pending, Running,
	// Unknown, or with no phase recorded.
	CountRunning PodCount = "running"
	// CountAll counts every Pod, finished ones included.
	CountAll PodCount = "all"
)

// Options tunes Classify.
type Options struct {
	// CountPods defaults to CountRunning when empty.
	CountPods PodCount
	// MinAge drops orphans younger than this. Zero keeps all. A claim whose
	// creation time cannot be parsed is always kept.
	MinAge time.Duration
	// Now anchors MinAge (injected so tests are deterministic).
	Now time.Time
}

// NoStorageClass is the display key for a PVC with no storage class set.
const NoStorageClass = "<none>"

// key uniquely identifies a claim within the cluster.
func key(namespace, name string) string { return namespace + "/" + name }

// parseBytes converts a Kubernetes quantity string ("10Gi", "500Mi", "1Ti")
// into a byte count. An empty or unparseable value yields 0.
func parseBytes(capacity string) int64 {
	if capacity == "" {
		return 0
	}
	q, err := resource.ParseQuantity(capacity)
	if err != nil {
		return 0
	}
	return q.Value()
}

// finished reports whether a Pod phase is terminal. Pods with an empty phase
// (e.g. a dump without status) are treated as active, so they keep their
// claims referenced.
func finished(phase string) bool {
	return phase == "Succeeded" || phase == "Failed"
}

// newer reports whether pod a was created after pod b, falling back to name
// order so the pick is stable.
func newer(a, b Pod) bool {
	ta, errA := time.Parse(time.RFC3339, a.CreationTimestamp)
	tb, errB := time.Parse(time.RFC3339, b.CreationTimestamp)
	switch {
	case errA == nil && errB == nil && !ta.Equal(tb):
		return ta.After(tb)
	case errA == nil && errB != nil:
		return true
	case errA != nil && errB == nil:
		return false
	}
	return a.Name < b.Name
}

// statefulSetOwned reports whether name is "<template>-<sts>-<ordinal>" for a
// StatefulSet in the same namespace.
func statefulSetOwned(pvc PVC, sets []StatefulSet) bool {
	for _, s := range sets {
		if s.Namespace != pvc.Namespace {
			continue
		}
		for _, tpl := range s.ClaimTemplates {
			prefix := tpl + "-" + s.Name + "-"
			if !strings.HasPrefix(pvc.Name, prefix) {
				continue
			}
			ord := pvc.Name[len(prefix):]
			if _, err := strconv.ParseUint(ord, 10, 32); err == nil {
				return true
			}
		}
	}
	return false
}

// Classify partitions PVCs into orphaned and referenced. A PVC is referenced
// when some counted Pod in the same namespace mounts a claim of the same name
// (see Options.CountPods). An unreferenced PVC that a live StatefulSet still
// owns is skipped and counted in StatefulSetRetained; every other PVC is
// orphaned. The result is sorted by namespace then name so output is stable
// regardless of input ordering.
func Classify(inv Inventory, opts Options) Result {
	count := opts.CountPods
	if count == "" {
		count = CountRunning
	}

	referenced := make(map[string]bool)
	lastFinished := make(map[string]Pod)
	for _, p := range inv.Pods {
		for _, claim := range p.ClaimRefs {
			if claim == "" {
				continue
			}
			k := key(p.Namespace, claim)
			if count == CountAll || !finished(p.Phase) {
				referenced[k] = true
				continue
			}
			if prev, ok := lastFinished[k]; !ok || newer(p, prev) {
				lastFinished[k] = p
			}
		}
	}

	res := Result{ByStorageClass: map[string]int64{}}
	for _, pvc := range inv.PVCs {
		k := key(pvc.Namespace, pvc.Name)
		if referenced[k] {
			continue
		}
		if statefulSetOwned(pvc, inv.StatefulSets) {
			res.StatefulSetRetained++
			continue
		}
		if opts.MinAge > 0 {
			if t, err := time.Parse(time.RFC3339, pvc.CreationTimestamp); err == nil && opts.Now.Sub(t) < opts.MinAge {
				continue
			}
		}

		reason := ReasonUnreferenced
		lastUsedBy := ""
		if p, ok := lastFinished[k]; ok {
			reason = ReasonFinishedOnly
			lastUsedBy = p.Name
		} else if pvc.Phase == "Pending" {
			reason = ReasonNeverBound
		}

		bytes := parseBytes(pvc.Capacity)
		sc := pvc.StorageClass
		if sc == "" {
			sc = NoStorageClass
		}
		res.Orphans = append(res.Orphans, Orphan{
			Name:              pvc.Name,
			Namespace:         pvc.Namespace,
			Phase:             pvc.Phase,
			StorageClass:      pvc.StorageClass,
			AccessModes:       pvc.AccessModes,
			Capacity:          pvc.Capacity,
			CapacityBytes:     bytes,
			CreationTimestamp: pvc.CreationTimestamp,
			Reason:            reason,
			LastUsedBy:        lastUsedBy,
		})
		res.TotalBytes += bytes
		res.ByStorageClass[sc] += bytes
	}

	sort.Slice(res.Orphans, func(i, j int) bool {
		if res.Orphans[i].Namespace != res.Orphans[j].Namespace {
			return res.Orphans[i].Namespace < res.Orphans[j].Namespace
		}
		return res.Orphans[i].Name < res.Orphans[j].Name
	})
	return res
}

// ParseAge parses a --min-age value: a Go duration ("12h", "90m") or a whole
// number of days ("30d").
func ParseAge(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.ParseUint(days, 10, 16)
		if err != nil {
			return 0, fmt.Errorf("invalid age %q: want e.g. 30d or 12h", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("invalid age %q: want e.g. 30d or 12h", s)
	}
	return d, nil
}

// FilterNamespaces keeps only objects in the given namespaces. An empty list
// keeps everything.
func (inv Inventory) FilterNamespaces(namespaces []string) Inventory {
	if len(namespaces) == 0 {
		return inv
	}
	keep := make(map[string]bool, len(namespaces))
	for _, ns := range namespaces {
		keep[ns] = true
	}
	var out Inventory
	for _, p := range inv.PVCs {
		if keep[p.Namespace] {
			out.PVCs = append(out.PVCs, p)
		}
	}
	for _, p := range inv.Pods {
		if keep[p.Namespace] {
			out.Pods = append(out.Pods, p)
		}
	}
	for _, s := range inv.StatefulSets {
		if keep[s.Namespace] {
			out.StatefulSets = append(out.StatefulSets, s)
		}
	}
	return out
}
