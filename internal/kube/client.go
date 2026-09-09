// Package kube loads live PVCs and Pods from a cluster via client-go and
// converts them into finder domain types.
package kube

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/moveeeax/k8s-orphaned-pvc-finder/internal/finder"
)

// NewClientset builds a Kubernetes clientset from the in-cluster config when
// running inside a Pod, otherwise from the supplied kubeconfig path (empty ==
// the standard loading rules, i.e. $KUBECONFIG then ~/.kube/config).
func NewClientset(kubeconfig string) (*kubernetes.Clientset, error) {
	if cfg, err := rest.InClusterConfig(); err == nil {
		return kubernetes.NewForConfig(cfg)
	}

	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		rules.ExplicitPath = kubeconfig
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		rules, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	return kubernetes.NewForConfig(cfg)
}

// Fetch lists PVCs and Pods in the given namespace (empty == all namespaces)
// and converts them into finder types.
func Fetch(ctx context.Context, cs kubernetes.Interface, namespace string) ([]finder.PVC, []finder.Pod, error) {
	pvcList, err := cs.CoreV1().PersistentVolumeClaims(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("list PVCs: %w", err)
	}
	podList, err := cs.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("list Pods: %w", err)
	}

	pvcs := make([]finder.PVC, 0, len(pvcList.Items))
	for _, p := range pvcList.Items {
		sc := ""
		if p.Spec.StorageClassName != nil {
			sc = *p.Spec.StorageClassName
		}
		capacity := ""
		if q, ok := p.Status.Capacity["storage"]; ok {
			capacity = q.String()
		}
		pvcs = append(pvcs, finder.PVC{
			Name:              p.Name,
			Namespace:         p.Namespace,
			Phase:             string(p.Status.Phase),
			StorageClass:      sc,
			Capacity:          capacity,
			CreationTimestamp: p.CreationTimestamp.Format("2006-01-02T15:04:05Z07:00"),
		})
	}

	pods := make([]finder.Pod, 0, len(podList.Items))
	for _, p := range podList.Items {
		var refs []string
		for _, v := range p.Spec.Volumes {
			if v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName != "" {
				refs = append(refs, v.PersistentVolumeClaim.ClaimName)
			}
		}
		pods = append(pods, finder.Pod{
			Name:      p.Name,
			Namespace: p.Namespace,
			ClaimRefs: refs,
		})
	}
	return pvcs, pods, nil
}
