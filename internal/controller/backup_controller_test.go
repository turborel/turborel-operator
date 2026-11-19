package controller

import (
	"context"
	"encoding/json"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	db "github.com/turborel/turborel-operator/api/v1alpha1"
	"github.com/turborel/turborel-operator/internal/instance"
)

func TestBackupCompletionRequiresVerifiedResult(t *testing.T) {
	for _, test := range []struct {
		name     string
		verified bool
		want     string
	}{{name: "verified", verified: true, want: "Completed"}, {name: "unverified", want: "Failed"}} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			cluster := testCluster()
			cluster.Spec.Backup = &db.BackupStorageSpec{ClaimName: "repository"}
			cluster.Spec.KeysSecret = "keys"
			cluster.Spec.ChecksumKey = "MASTER"
			cluster.Status.Primary = "demo-1"
			cluster.Status.ReadWriteService = "demo-rw.default.svc"
			setCondition(cluster, "Ready", true, "Available", "Ready")
			backup := &db.Backup{ObjectMeta: metav1.ObjectMeta{Name: "backup", Namespace: "default", UID: "backup-uid"}, Spec: db.BackupSpec{ClusterName: cluster.Name, Verify: ptr.To(true)}}
			c, scheme := testClient(t, cluster, backup)
			r := &BackupReconciler{Client: c, Scheme: scheme, OperatorImage: "operator:test"}
			req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(backup)}
			for range 2 {
				if _, err := r.Reconcile(ctx, req); err != nil {
					t.Fatal(err)
				}
			}
			jobs := &batchv1.JobList{}
			if err := c.List(ctx, jobs); err != nil {
				t.Fatal(err)
			}
			if len(jobs.Items) != 1 {
				t.Fatalf("created %d Jobs", len(jobs.Items))
			}
			job := &jobs.Items[0]
			var config instance.BackupConfig
			if err := json.Unmarshal([]byte(job.Spec.Template.Spec.Containers[0].Env[0].Value), &config); err != nil {
				t.Fatal(err)
			}
			if !config.Keys || config.ChecksumKey != "MASTER" {
				t.Fatal("verification must register the source encryption and checksum keys")
			}
			if *job.Spec.BackoffLimit != 0 {
				t.Fatal("ambiguous backup failures must not retry automatically")
			}
			job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
			if err := c.Status().Update(ctx, job); err != nil {
				t.Fatal(err)
			}
			// A Job event can arrive before the Pod's termination message.
			if _, err := r.Reconcile(ctx, req); err != nil {
				t.Fatal(err)
			}
			if err := c.Get(ctx, req.NamespacedName, backup); err != nil {
				t.Fatal(err)
			}
			if backup.Status.Phase != "Running" {
				t.Fatal("cache lag was mistaken for a missing backup result")
			}
			result, err := json.Marshal(instance.BackupResult{BackupID: "B123", Verified: test.verified})
			if err != nil {
				t.Fatal(err)
			}
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "backup-pod", Namespace: "default", Labels: map[string]string{"batch.kubernetes.io/job-name": job.Name},
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID, Controller: ptr.To(true)}}},
				Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "backup", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Message: string(result)}}}}}}
			if err := c.Create(ctx, pod); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Reconcile(ctx, req); err != nil {
				t.Fatal(err)
			}
			if err := c.Get(ctx, req.NamespacedName, backup); err != nil {
				t.Fatal(err)
			}
			if backup.Status.Phase != test.want {
				t.Fatalf("phase %s want %s", backup.Status.Phase, test.want)
			}
			if err := c.Delete(ctx, job); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Reconcile(ctx, req); err != nil {
				t.Fatal(err)
			}
			if err := c.List(ctx, jobs); err != nil {
				t.Fatal(err)
			}
			if len(jobs.Items) != 0 {
				t.Fatal("terminal backup was run again")
			}
		})
	}
}
