package controller

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	db "github.com/turborel/turborel-operator/api/v1alpha1"
	"github.com/turborel/turborel-operator/internal/instance"
)

type uidClient struct{ client.Client }

func (c uidClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if obj.GetUID() == "" {
		obj.SetUID(types.UID("uid-" + obj.GetName()))
	}
	return c.Client.Create(ctx, obj, opts...)
}
func testClient(t *testing.T, objects ...client.Object) (client.Client, *runtime.Scheme) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := db.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&db.Cluster{}, &db.Backup{}, &db.ScheduledBackup{}, &corev1.Pod{}).
		WithObjects(objects...).Build()
	return uidClient{Client: c}, scheme
}
func testCluster() *db.Cluster {
	return &db.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default", UID: "cluster-uid", Generation: 1},
		Spec: db.ClusterSpec{Image: "turborel:test", Instances: 3, OwnerSecret: "owner", Storage: db.StorageSpec{Size: quantity("1Gi")}}}
}
func ownerSecret() *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "owner", Namespace: "default"}, Data: map[string][]byte{"username": []byte("admin"), "password": []byte("test-password")}}
}
func reconcile(t *testing.T, r *ClusterReconciler) {
	t.Helper()
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "demo", Namespace: "default"}}); err != nil {
		t.Fatal(err)
	}
}
func makeRunning(t *testing.T, c client.Client) {
	t.Helper()
	pods := &corev1.PodList{}
	if err := c.List(context.Background(), pods); err != nil {
		t.Fatal(err)
	}
	for i := range pods.Items {
		p := &pods.Items[i]
		p.Status.Phase = corev1.PodRunning
		p.Status.PodIP = "127.0.0.1"
		if err := c.Status().Update(context.Background(), p); err != nil {
			t.Fatal(err)
		}
	}
}
func TestClusterLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c, scheme := testClient(t, testCluster(), ownerSecret())
	primary := "demo-1"
	r := &ClusterReconciler{Client: c, Scheme: scheme, OperatorImage: "operator:test", Inspect: func(_ context.Context, _ *db.Cluster, p *corev1.Pod, _ map[string][]byte) (instance.Status, error) {
		role := "replica"
		if p.Name == primary {
			role = "primary"
		}
		return instance.Status{Ready: true, Healthy: true, Members: 3, Role: role, CheckedAt: time.Now()}, nil
	}}
	reconcile(t, r)
	reconcile(t, r)
	makeRunning(t, c)
	reconcile(t, r)
	reconcile(t, r)
	cluster := &db.Cluster{}
	if err := c.Get(ctx, types.NamespacedName{Name: "demo", Namespace: "default"}, cluster); err != nil {
		t.Fatal(err)
	}
	if !cluster.Status.Initialized || cluster.Status.ReadyInstances != 3 || cluster.Status.Primary != "demo-1" {
		t.Fatalf("unexpected status: %+v", cluster.Status)
	}
	claims := &corev1.PersistentVolumeClaimList{}
	if err := c.List(ctx, claims); err != nil {
		t.Fatal(err)
	}
	if len(claims.Items) != 3 {
		t.Fatalf("got %d volumes", len(claims.Items))
	}
	for _, p := range claims.Items {
		if len(p.OwnerReferences) != 0 {
			t.Fatal("data volume must survive cluster deletion")
		}
	}
	pdb := &policyv1.PodDisruptionBudget{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(cluster), pdb); err != nil {
		t.Fatal(err)
	}
	if pdb.Spec.MinAvailable.IntVal != 2 {
		t.Fatal("PDB does not preserve quorum")
	}
	// A native election changes service routing without restarting a member.
	primary = "demo-2"
	reconcile(t, r)
	for _, name := range []string{"demo-1", "demo-2", "demo-3"} {
		p := &corev1.Pod{}
		if err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, p); err != nil {
			t.Fatal(err)
		}
		want := "replica"
		if name == primary {
			want = "primary"
		}
		if p.Labels[roleLabel] != want {
			t.Fatalf("%s has role %s", name, p.Labels[roleLabel])
		}
	}
	// An image update removes one replica, preserving the elected primary.
	if err := c.Get(ctx, client.ObjectKeyFromObject(cluster), cluster); err != nil {
		t.Fatal(err)
	}
	cluster.Spec.Image = "turborel:next"
	if err := c.Update(ctx, cluster); err != nil {
		t.Fatal(err)
	}
	reconcile(t, r)
	pods := &corev1.PodList{}
	if err := c.List(ctx, pods); err != nil {
		t.Fatal(err)
	}
	if len(pods.Items) != 2 {
		t.Fatalf("expected one replacement, got %d pods", len(pods.Items))
	}
	if err := c.Get(ctx, types.NamespacedName{Name: primary, Namespace: "default"}, &corev1.Pod{}); err != nil {
		t.Fatal("primary replaced before replicas")
	}
	reconcile(t, r) // recreate the member, which is not yet healthy
	reconcile(t, r) // must not delete another member
	if err := c.List(ctx, pods); err != nil {
		t.Fatal(err)
	}
	if len(pods.Items) != 3 {
		t.Fatal("rollout continued with an unready member")
	}
}
func TestMissingOrReplacedVolumeFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name        string
		replacement bool
	}{{name: "missing"}, {name: "replaced", replacement: true}} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			c, scheme := testClient(t, testCluster(), ownerSecret())
			r := &ClusterReconciler{Client: c, Scheme: scheme, OperatorImage: "operator:test"}
			reconcile(t, r)
			key := types.NamespacedName{Name: "demo-1", Namespace: "default"}
			claim := &corev1.PersistentVolumeClaim{}
			if err := c.Get(ctx, key, claim); err != nil {
				t.Fatal(err)
			}
			if err := c.Delete(ctx, claim); err != nil {
				t.Fatal(err)
			}
			if test.replacement {
				claim.ResourceVersion = ""
				claim.UID = "different"
				if err := c.Create(ctx, claim); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "demo", Namespace: "default"}}); err == nil {
				t.Fatal("accepted lost/replaced data volume")
			}
			if !test.replacement {
				if err := c.Get(ctx, key, &corev1.PersistentVolumeClaim{}); !apierrors.IsNotFound(err) {
					t.Fatal("missing volume was silently recreated")
				}
			}
			pods := &corev1.PodList{}
			if err := c.List(ctx, pods); err != nil {
				t.Fatal(err)
			}
			if len(pods.Items) != 0 {
				t.Fatal("started database on untrusted volume")
			}
		})
	}
}
func TestUnsafeConfigurationRejected(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*db.Cluster)
	}{
		{name: "even quorum", mutate: func(c *db.Cluster) { c.Spec.Instances = 2 }},
		{name: "reserved setting", mutate: func(c *db.Cluster) { c.Spec.Settings = map[string]string{"SERVER.TRUST_LOCAL": "ON"} }},
		{name: "zero storage", mutate: func(c *db.Cluster) { c.Spec.Storage.Size = quantity("0") }},
		{name: "replicated archive", mutate: func(c *db.Cluster) { c.Spec.Backup = &db.BackupStorageSpec{ClaimName: "backups", Archive: true} }},
		{name: "replicated restore", mutate: func(c *db.Cluster) { c.Spec.Restore = &db.RestoreSpec{ClaimName: "backup", BackupPath: "full"} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			c := testCluster()
			test.mutate(c)
			if err := validateCluster(c); err == nil {
				t.Fatal("unsafe configuration accepted")
			}
		})
	}
}

func TestRolloutRequiresAgreementOnMembershipAndView(t *testing.T) {
	t.Parallel()
	states := map[string]instance.Status{
		"one":   {Ready: true, Healthy: true, Members: 3, Epoch: 2, View: 5},
		"two":   {Ready: true, Healthy: true, Members: 3, Epoch: 2, View: 5},
		"three": {Ready: true, Healthy: true, Members: 3, Epoch: 2, View: 5},
	}
	if !membersAgree(states, 3) {
		t.Fatal("healthy group rejected")
	}
	state := states["three"]
	state.View++
	states["three"] = state
	if membersAgree(states, 3) {
		t.Fatal("different views allowed another member to roll")
	}
	state.View--
	state.Members = 5
	states["three"] = state
	if membersAgree(states, 3) {
		t.Fatal("unmanaged membership change allowed rollout")
	}
	state.Members = 3
	state.Healthy = false
	states["three"] = state
	if membersAgree(states, 3) {
		t.Fatal("stale primary link allowed rollout")
	}
}
