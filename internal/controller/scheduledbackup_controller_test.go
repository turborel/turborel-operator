package controller

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	db "github.com/turborel/turborel-operator/api/v1alpha1"
)

func TestScheduledBackupRecoversAfterStatusWriteFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	schedule := &db.ScheduledBackup{ObjectMeta: metav1.ObjectMeta{Name: "daily", Namespace: "default", UID: "12345678-abcd", CreationTimestamp: metav1.NewTime(time.Now().Add(-2 * time.Hour))},
		Spec: db.ScheduledBackupSpec{ClusterName: "demo", EverySeconds: 3600}}
	c, scheme := testClient(t, schedule)
	r := &ScheduledBackupReconciler{Client: c, Scheme: scheme}
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(schedule)}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, req.NamespacedName, schedule); err != nil {
		t.Fatal(err)
	}
	name := schedule.Status.LastBackup
	schedule.Status = db.ScheduledBackupStatus{}
	if err := c.Status().Update(ctx, schedule); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	backups := &db.BackupList{}
	if err := c.List(ctx, backups); err != nil {
		t.Fatal(err)
	}
	if len(backups.Items) != 1 || backups.Items[0].Name != name {
		t.Fatal("recovery duplicated a scheduled backup")
	}
	if err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, &db.Backup{}); err != nil {
		t.Fatal(err)
	}
}
