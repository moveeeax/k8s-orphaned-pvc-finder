package finder

import (
	"fmt"

	"sigs.k8s.io/yaml"
)

// Dump is the offline input format: raw Kubernetes PVC, Pod and StatefulSet
// objects grouped under three keys. It is what you get from
//
//	{ "pvcs": $(kubectl get pvc -o json).items,
//	  "pods": $(kubectl get pods -o json).items,
//	  "statefulsets": $(kubectl get sts -o json).items }
//
// The loader reads the fields it needs and ignores the rest, so a verbatim
// `kubectl get -o json` object works without trimming. "statefulsets" is
// optional; without it no claim is skipped as StatefulSet-retained.
type Dump struct {
	PVCs         []rawPVC         `json:"pvcs"`
	Pods         []rawPod         `json:"pods"`
	StatefulSets []rawStatefulSet `json:"statefulsets"`
}

type objectMeta struct {
	Name              string `json:"name"`
	Namespace         string `json:"namespace"`
	CreationTimestamp string `json:"creationTimestamp"`
}

type rawPVC struct {
	Metadata objectMeta `json:"metadata"`
	Spec     struct {
		StorageClassName *string  `json:"storageClassName"`
		AccessModes      []string `json:"accessModes"`
	} `json:"spec"`
	Status struct {
		Phase       string            `json:"phase"`
		AccessModes []string          `json:"accessModes"`
		Capacity    map[string]string `json:"capacity"`
	} `json:"status"`
}

type rawPod struct {
	Metadata objectMeta `json:"metadata"`
	Spec     struct {
		Volumes []struct {
			PersistentVolumeClaim *struct {
				ClaimName string `json:"claimName"`
			} `json:"persistentVolumeClaim"`
		} `json:"volumes"`
	} `json:"spec"`
	Status struct {
		Phase string `json:"phase"`
	} `json:"status"`
}

type rawStatefulSet struct {
	Metadata objectMeta `json:"metadata"`
	Spec     struct {
		VolumeClaimTemplates []struct {
			Metadata objectMeta `json:"metadata"`
		} `json:"volumeClaimTemplates"`
	} `json:"spec"`
}

// ParseDump reads a JSON or YAML Dump and converts it into finder domain types.
// sigs.k8s.io/yaml accepts JSON as a subset of YAML, so both formats work.
func ParseDump(data []byte) (Inventory, error) {
	var d Dump
	if err := yaml.Unmarshal(data, &d); err != nil {
		return Inventory{}, fmt.Errorf("parse dump: %w", err)
	}

	inv := Inventory{
		PVCs:         make([]PVC, 0, len(d.PVCs)),
		Pods:         make([]Pod, 0, len(d.Pods)),
		StatefulSets: make([]StatefulSet, 0, len(d.StatefulSets)),
	}
	for _, r := range d.PVCs {
		sc := ""
		if r.Spec.StorageClassName != nil {
			sc = *r.Spec.StorageClassName
		}
		// status.accessModes is what the bound volume actually offers; fall
		// back to the requested modes for an unbound claim.
		modes := r.Status.AccessModes
		if len(modes) == 0 {
			modes = r.Spec.AccessModes
		}
		inv.PVCs = append(inv.PVCs, PVC{
			Name:              r.Metadata.Name,
			Namespace:         r.Metadata.Namespace,
			Phase:             r.Status.Phase,
			StorageClass:      sc,
			AccessModes:       modes,
			Capacity:          r.Status.Capacity["storage"],
			CreationTimestamp: r.Metadata.CreationTimestamp,
		})
	}

	for _, r := range d.Pods {
		var refs []string
		for _, v := range r.Spec.Volumes {
			if v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName != "" {
				refs = append(refs, v.PersistentVolumeClaim.ClaimName)
			}
		}
		inv.Pods = append(inv.Pods, Pod{
			Name:              r.Metadata.Name,
			Namespace:         r.Metadata.Namespace,
			Phase:             r.Status.Phase,
			CreationTimestamp: r.Metadata.CreationTimestamp,
			ClaimRefs:         refs,
		})
	}

	for _, r := range d.StatefulSets {
		var tpls []string
		for _, t := range r.Spec.VolumeClaimTemplates {
			if t.Metadata.Name != "" {
				tpls = append(tpls, t.Metadata.Name)
			}
		}
		inv.StatefulSets = append(inv.StatefulSets, StatefulSet{
			Name:           r.Metadata.Name,
			Namespace:      r.Metadata.Namespace,
			ClaimTemplates: tpls,
		})
	}
	return inv, nil
}
