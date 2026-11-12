// Package kube loads live PVCs, Pods and StatefulSets from a cluster via
// client-go and converts them into finder domain types.
package kube

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/moveeeax/k8s-orphaned-pvc-finder/internal/finder"
)

// NewClientset builds a Kubernetes clientset from the in-cluster config when
// running inside a Pod, otherwise from the supplied kubeconfig path (empty ==
// the standard loading rules, i.e. $KUBECONFIG then ~/.kube/config). It also
// returns the namespace of the current context (in a Pod: the service
// account's namespace), used when no namespace flag is given.
func NewClientset(kubeconfig string) (*kubernetes.Clientset, string, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		rules.ExplicitPath = kubeconfig
	}
	loader := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{})

	if cfg, err := rest.InClusterConfig(); err == nil {
		ns, _, err := loader.Namespace()
		if err != nil {
			ns = "default"
		}
		cs, err := kubernetes.NewForConfig(cfg)
		return cs, ns, err
	}

	cfg, err := loader.ClientConfig()
	if err != nil {
		return nil, "", fmt.Errorf("load kubeconfig: %w", err)
	}
	ns, _, err := loader.Namespace()
	if err != nil {
		return nil, "", fmt.Errorf("resolve namespace: %w", err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	return cs, ns, err
}

// Fetch lists PVCs, Pods and StatefulSets in each given namespace (an empty
// string == all namespaces) and converts them into finder types. Not being
// allowed to list StatefulSets is not fatal: warn is called and the scan goes
// on without the StatefulSet skip, so scaled-down replicas show as orphans
// rather than hiding real ones.
func Fetch(ctx context.Context, cs kubernetes.Interface, namespaces []string, warn func(string)) (finder.Inventory, error) {
	var inv finder.Inventory
	for _, ns := range namespaces {
		if err := fetchNamespace(ctx, cs, ns, &inv, warn); err != nil {
			return finder.Inventory{}, err
		}
	}
	return inv, nil
}

func fetchNamespace(ctx context.Context, cs kubernetes.Interface, namespace string, inv *finder.Inventory, warn func(string)) error {
	pvcList, err := cs.CoreV1().PersistentVolumeClaims(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list PVCs: %w", err)
	}
	podList, err := cs.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list Pods: %w", err)
	}
	stsList, err := cs.AppsV1().StatefulSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		if !apierrors.IsForbidden(err) {
			return fmt.Errorf("list StatefulSets: %w", err)
		}
		if warn != nil {
			warn(fmt.Sprintf("cannot list StatefulSets (%v); claims of scaled-down StatefulSet replicas will be reported as orphans", err))
		}
		stsList = nil
	}

	for _, p := range pvcList.Items {
		sc := ""
		if p.Spec.StorageClassName != nil {
			sc = *p.Spec.StorageClassName
		}
		capacity := ""
		if q, ok := p.Status.Capacity["storage"]; ok {
			capacity = q.String()
		}
		// status.accessModes is what the bound volume offers; fall back to the
		// requested modes for an unbound claim.
		raw := p.Status.AccessModes
		if len(raw) == 0 {
			raw = p.Spec.AccessModes
		}
		var modes []string
		for _, m := range raw {
			modes = append(modes, string(m))
		}
		inv.PVCs = append(inv.PVCs, finder.PVC{
			Name:              p.Name,
			Namespace:         p.Namespace,
			Phase:             string(p.Status.Phase),
			StorageClass:      sc,
			AccessModes:       modes,
			Capacity:          capacity,
			CreationTimestamp: p.CreationTimestamp.Format(time.RFC3339),
		})
	}

	for _, p := range podList.Items {
		var refs []string
		for _, v := range p.Spec.Volumes {
			if v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName != "" {
				refs = append(refs, v.PersistentVolumeClaim.ClaimName)
			}
		}
		inv.Pods = append(inv.Pods, finder.Pod{
			Name:              p.Name,
			Namespace:         p.Namespace,
			Phase:             string(p.Status.Phase),
			CreationTimestamp: p.CreationTimestamp.Format(time.RFC3339),
			ClaimRefs:         refs,
		})
	}

	if stsList != nil {
		for _, s := range stsList.Items {
			var tpls []string
			for _, t := range s.Spec.VolumeClaimTemplates {
				if t.Name != "" {
					tpls = append(tpls, t.Name)
				}
			}
			inv.StatefulSets = append(inv.StatefulSets, finder.StatefulSet{
				Name:           s.Name,
				Namespace:      s.Namespace,
				ClaimTemplates: tpls,
			})
		}
	}
	return nil
}
