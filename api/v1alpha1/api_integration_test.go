//go:build integration

package v1alpha1_test

import (
	"context"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	db "github.com/turborel/turborel-operator/api/v1alpha1"
)

func TestAPIValidation(t *testing.T) {
	environment := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", ".test", "crds")}, ErrorIfCRDPathMissing: true}
	cfg, err := environment.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := environment.Stop(); err != nil {
			t.Error(err)
		}
	})
	scheme := runtime.NewScheme()
	if err := db.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "validation"}}
	if err := c.Create(ctx, namespace); err != nil {
		t.Fatal(err)
	}
	cluster := testCluster()
	cluster.Name = "defaults"
	cluster.Namespace = namespace.Name
	cluster.UID = ""
	cluster.Spec.Instances = 0
	if err := c.Create(ctx, cluster); err != nil {
		t.Fatal(err)
	}
	if cluster.Spec.Instances != 1 {
		t.Fatalf("instances default = %d", cluster.Spec.Instances)
	}
	cluster.Spec.Instances = 3
	if err := c.Update(ctx, cluster); !apierrors.IsInvalid(err) {
		t.Fatalf("membership change accepted: %v", err)
	}
	cluster.Spec.Instances = 1
	cluster.Spec.Storage.StorageClassName = ptr.To("different")
	if err := c.Update(ctx, cluster); !apierrors.IsInvalid(err) {
		t.Fatalf("storage class addition accepted: %v", err)
	}
	dotted := testCluster()
	dotted.Name = "has.dots"
	dotted.Namespace = namespace.Name
	dotted.UID = ""
	if err := c.Create(ctx, dotted); !apierrors.IsInvalid(err) {
		t.Fatalf("invalid Pod hostname accepted: %v", err)
	}
	for _, test := range []struct {
		name     string
		settings map[string]string
	}{
		{name: "network", settings: map[string]string{"SERVER.TRUST_LOCAL": "ON"}},
		{name: "replication", settings: map[string]string{"REPLICATION.TLS": "OFF"}},
		{name: "case", settings: map[string]string{"storage.page_bytes": "4096"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			bad := testCluster()
			bad.Name = test.name
			bad.Namespace = namespace.Name
			bad.UID = ""
			bad.Spec.Settings = test.settings
			if err := c.Create(ctx, bad); !apierrors.IsInvalid(err) {
				t.Fatalf("unsafe setting accepted: %v", err)
			}
		})
	}
	recovery := testCluster()
	recovery.Name = "restore"
	recovery.Namespace = namespace.Name
	recovery.UID = ""
	recovery.Spec.Instances = 1
	recovery.Spec.Restore = &db.RestoreSpec{ClaimName: "backups", BackupPath: "full", UntilTime: "2026-11-19 12:00:00"}
	if err := c.Create(ctx, recovery); !apierrors.IsInvalid(err) {
		t.Fatalf("PITR without archive accepted: %v", err)
	}
	recovery.Spec.Restore.ArchivePath = "archive"
	if err := c.Create(ctx, recovery); err != nil {
		t.Fatal(err)
	}
	recovery.Spec.Restore = nil
	if err := c.Update(ctx, recovery); !apierrors.IsInvalid(err) {
		t.Fatalf("restore removal accepted: %v", err)
	}
	backup := &db.Backup{ObjectMeta: metav1.ObjectMeta{Name: "one", Namespace: namespace.Name}, Spec: db.BackupSpec{ClusterName: "defaults"}}
	if err := c.Create(ctx, backup); err != nil {
		t.Fatal(err)
	}
	if backup.Spec.Verify == nil || !*backup.Spec.Verify || backup.Spec.DeadlineSeconds != 3600 {
		t.Fatal("backup defaults not applied")
	}
	backup.Spec.ClusterName = "another"
	if err := c.Update(ctx, backup); !apierrors.IsInvalid(err) {
		t.Fatalf("backup source mutation accepted: %v", err)
	}
}

func testCluster() *db.Cluster {
	return &db.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
		Spec: db.ClusterSpec{Image: "turborel:test", Instances: 3, OwnerSecret: "owner",
			Storage: db.StorageSpec{Size: resource.MustParse("1Gi")}}}
}
