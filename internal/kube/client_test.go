package kube

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func objects() []runtime.Object {
	gp3 := "gp3"
	return []runtime.Object{
		&corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "data-db-1", Namespace: "db"},
			Spec: corev1.PersistentVolumeClaimSpec{
				StorageClassName: &gp3,
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			},
			Status: corev1.PersistentVolumeClaimStatus{
				Phase:       corev1.ClaimBound,
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOncePod},
				Capacity:    corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("8Gi")},
			},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "job-1", Namespace: "db"},
			Spec: corev1.PodSpec{Volumes: []corev1.Volume{{
				Name:         "data",
				VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data-db-1"}},
			}}},
			Status: corev1.PodStatus{Phase: corev1.PodSucceeded},
		},
		&appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "db"},
			Spec: appsv1.StatefulSetSpec{VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
				{ObjectMeta: metav1.ObjectMeta{Name: "data"}},
			}},
		},
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "web"}},
	}
}

func TestFetchConvertsObjects(t *testing.T) {
	cs := fake.NewClientset(objects()...)
	inv, err := Fetch(context.Background(), cs, []string{"db"}, nil)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(inv.PVCs) != 1 {
		t.Fatalf("got %d PVCs, want 1 (namespace-scoped)", len(inv.PVCs))
	}
	p := inv.PVCs[0]
	if p.StorageClass != "gp3" || p.Capacity != "8Gi" || p.Phase != "Bound" {
		t.Errorf("pvc = %+v", p)
	}
	// status.accessModes wins over the requested spec.accessModes.
	if len(p.AccessModes) != 1 || p.AccessModes[0] != "ReadWriteOncePod" {
		t.Errorf("accessModes = %v", p.AccessModes)
	}
	if len(inv.Pods) != 1 || inv.Pods[0].Phase != "Succeeded" || inv.Pods[0].ClaimRefs[0] != "data-db-1" {
		t.Errorf("pods = %+v", inv.Pods)
	}
	if len(inv.StatefulSets) != 1 || inv.StatefulSets[0].ClaimTemplates[0] != "data" {
		t.Errorf("statefulsets = %+v", inv.StatefulSets)
	}
}

func TestFetchSeveralNamespaces(t *testing.T) {
	cs := fake.NewClientset(objects()...)
	inv, err := Fetch(context.Background(), cs, []string{"db", "web"}, nil)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(inv.PVCs) != 2 {
		t.Errorf("got %d PVCs, want 2", len(inv.PVCs))
	}
}

func TestFetchWarnsWhenStatefulSetsForbidden(t *testing.T) {
	cs := fake.NewClientset(objects()...)
	cs.PrependReactor("list", "statefulsets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "statefulsets"}, "", nil)
	})
	var warnings []string
	inv, err := Fetch(context.Background(), cs, []string{"db"}, func(m string) { warnings = append(warnings, m) })
	if err != nil {
		t.Fatalf("forbidden StatefulSet list must not fail the scan: %v", err)
	}
	if len(inv.PVCs) != 1 || len(inv.StatefulSets) != 0 {
		t.Errorf("inventory = %+v", inv)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "StatefulSets") {
		t.Errorf("warnings = %v", warnings)
	}
}
