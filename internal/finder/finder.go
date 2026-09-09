// Package finder holds the pure orphaned-PVC classification logic.
//
// It is deliberately free of any Kubernetes client dependency: it operates on
// small domain structs so the core can be unit-tested offline against fixtures
// with no API server. The live-cluster and dump loaders both convert into these
// types and hand them to Classify.
package finder

import (
	"sort"

	"k8s.io/apimachinery/pkg/api/resource"
)

// PVC is the minimal view of a PersistentVolumeClaim the finder needs.
type PVC struct {
	Name              string `json:"name"`
	Namespace         string `json:"namespace"`
	Phase             string `json:"phase"`
	StorageClass      string `json:"storageClass"`
	Capacity          string `json:"capacity"`
	CreationTimestamp string `json:"creationTimestamp"`
}

// Pod is the minimal view of a Pod: the PVC names it mounts.
type Pod struct {
	Name      string   `json:"name"`
	Namespace string   `json:"namespace"`
	ClaimRefs []string `json:"claimRefs"`
}

// Orphan is a PVC that no Pod references.
type Orphan struct {
	Name              string `json:"name"`
	Namespace         string `json:"namespace"`
	Phase             string `json:"phase"`
	StorageClass      string `json:"storageClass"`
	Capacity          string `json:"capacity"`
	CapacityBytes     int64  `json:"capacityBytes"`
	CreationTimestamp string `json:"creationTimestamp"`
}

// Result is the outcome of a scan.
type Result struct {
	Orphans        []Orphan         `json:"orphans"`
	TotalBytes     int64            `json:"totalBytes"`
	ByStorageClass map[string]int64 `json:"byStorageClass"`
}

// noStorageClass is the display key for a PVC with no storage class set.
const noStorageClass = "<none>"

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

// Classify partitions PVCs into orphaned and referenced. A PVC is referenced
// when some Pod in the same namespace mounts a claim of the same name; every
// other PVC is orphaned. The result is sorted by namespace then name so output
// is stable regardless of input ordering.
func Classify(pvcs []PVC, pods []Pod) Result {
	referenced := make(map[string]bool)
	for _, p := range pods {
		for _, claim := range p.ClaimRefs {
			if claim == "" {
				continue
			}
			referenced[key(p.Namespace, claim)] = true
		}
	}

	res := Result{ByStorageClass: map[string]int64{}}
	for _, pvc := range pvcs {
		if referenced[key(pvc.Namespace, pvc.Name)] {
			continue
		}
		bytes := parseBytes(pvc.Capacity)
		sc := pvc.StorageClass
		if sc == "" {
			sc = noStorageClass
		}
		res.Orphans = append(res.Orphans, Orphan{
			Name:              pvc.Name,
			Namespace:         pvc.Namespace,
			Phase:             pvc.Phase,
			StorageClass:      pvc.StorageClass,
			Capacity:          pvc.Capacity,
			CapacityBytes:     bytes,
			CreationTimestamp: pvc.CreationTimestamp,
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
