package finder

import (
	"fmt"

	"sigs.k8s.io/yaml"
)

// Dump is the offline input format: raw Kubernetes PVC and Pod objects grouped
// under two keys. It is what you get from
//
//	{ "pvcs": $(kubectl get pvc -o json).items, "pods": $(kubectl get pods -o json).items }
//
// The loader reads the fields it needs and ignores the rest, so a verbatim
// `kubectl get -o json` object works without trimming.
type Dump struct {
	PVCs []rawPVC `json:"pvcs"`
	Pods []rawPod `json:"pods"`
}

type objectMeta struct {
	Name              string `json:"name"`
	Namespace         string `json:"namespace"`
	CreationTimestamp string `json:"creationTimestamp"`
}

type rawPVC struct {
	Metadata objectMeta `json:"metadata"`
	Spec     struct {
		StorageClassName *string `json:"storageClassName"`
	} `json:"spec"`
	Status struct {
		Phase    string            `json:"phase"`
		Capacity map[string]string `json:"capacity"`
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
}

// ParseDump reads a JSON or YAML Dump and converts it into finder domain types.
// sigs.k8s.io/yaml accepts JSON as a subset of YAML, so both formats work.
func ParseDump(data []byte) ([]PVC, []Pod, error) {
	var d Dump
	if err := yaml.Unmarshal(data, &d); err != nil {
		return nil, nil, fmt.Errorf("parse dump: %w", err)
	}

	pvcs := make([]PVC, 0, len(d.PVCs))
	for _, r := range d.PVCs {
		sc := ""
		if r.Spec.StorageClassName != nil {
			sc = *r.Spec.StorageClassName
		}
		pvcs = append(pvcs, PVC{
			Name:              r.Metadata.Name,
			Namespace:         r.Metadata.Namespace,
			Phase:             r.Status.Phase,
			StorageClass:      sc,
			Capacity:          r.Status.Capacity["storage"],
			CreationTimestamp: r.Metadata.CreationTimestamp,
		})
	}

	pods := make([]Pod, 0, len(d.Pods))
	for _, r := range d.Pods {
		var refs []string
		for _, v := range r.Spec.Volumes {
			if v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName != "" {
				refs = append(refs, v.PersistentVolumeClaim.ClaimName)
			}
		}
		pods = append(pods, Pod{
			Name:      r.Metadata.Name,
			Namespace: r.Metadata.Namespace,
			ClaimRefs: refs,
		})
	}
	return pvcs, pods, nil
}
