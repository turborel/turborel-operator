package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	db "github.com/turborel/turborel-operator/api/v1alpha1"
	"github.com/turborel/turborel-operator/internal/instance"
)

type BackupReconciler struct {
	client.Client
	Scheme                  *runtime.Scheme
	OperatorImage           string
	OperatorImagePullPolicy corev1.PullPolicy
}

func (r *BackupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, retErr error) {
	b := &db.Backup{}
	if err := r.Get(ctx, req.NamespacedName, b); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !b.DeletionTimestamp.IsZero() || b.Status.Phase == "Completed" || b.Status.Phase == "Failed" {
		return ctrl.Result{}, nil
	}
	before := b.DeepCopy()
	defer func() {
		if !reflect.DeepEqual(before.Status, b.Status) {
			if err := r.Status().Patch(ctx, b, client.MergeFrom(before)); err != nil && retErr == nil {
				retErr = err
			}
		}
	}()
	fail := func(reason, message string) (ctrl.Result, error) { return failBackup(b, reason, message) }
	c := &db.Cluster{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: b.Namespace, Name: b.Spec.ClusterName}, c); err != nil {
		return ctrl.Result{}, err
	}
	if c.Spec.Backup == nil {
		return fail("NoRepository", "Cluster has no backup repository")
	}
	if b.Status.ClusterUID != "" && b.Status.ClusterUID != string(c.UID) {
		return fail("ClusterReplaced", "The source cluster identity changed")
	}
	parentID := ""
	repository := string(c.UID) + "/backups/" + string(b.UID)
	if b.Spec.Parent != "" {
		parent := &db.Backup{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: b.Namespace, Name: b.Spec.Parent}, parent); err != nil {
			return ctrl.Result{}, err
		}
		valid := parent.Status.Phase == "Completed" && parent.Status.BackupID != "" && parent.Status.ClusterUID == string(c.UID)
		if !valid || parent.Status.ClaimName != c.Spec.Backup.ClaimName {
			return fail("InvalidParent", "Parent must be a completed backup of this cluster and repository")
		}
		repository = parent.Status.RepositoryPath
		parentID = parent.Status.BackupID
	}
	if b.Status.Phase == "" {
		b.Status.Phase = "Pending"
		b.Status.RepositoryPath = repository
		b.Status.ClusterUID = string(c.UID)
		b.Status.ClaimName = c.Spec.Backup.ClaimName
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}
	if b.Status.ClaimName != c.Spec.Backup.ClaimName {
		return fail("RepositoryChanged", "The source repository changed before the backup finished")
	}
	jobName := "backup-" + string(b.UID)
	job := &batchv1.Job{}
	err := r.Get(ctx, types.NamespacedName{Namespace: b.Namespace, Name: jobName}, job)
	if apierrors.IsNotFound(err) {
		if b.Status.JobName != "" {
			return fail("JobLost", "Backup Job was removed; create a new Backup to retry")
		}
		if c.Status.Primary == "" || !meta.IsStatusConditionTrue(c.Status.Conditions, "Ready") {
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		job, err = r.createBackupJob(ctx, c, b, parentID)
		if err != nil {
			return ctrl.Result{}, err
		}
	} else if err != nil {
		return ctrl.Result{}, err
	}
	if !metav1.IsControlledBy(job, b) {
		return fail("JobConflict", "Job is not controlled by this Backup")
	}
	b.Status.JobName = jobName
	b.Status.Phase = "Running"
	if b.Status.StartedAt == nil {
		now := metav1.Now()
		b.Status.StartedAt = &now
	}
	return r.observeBackupJob(ctx, b, job)
}

func (r *BackupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&db.Backup{}).Owns(&batchv1.Job{}).Complete(r)
}

func (r *BackupReconciler) createBackupJob(
	ctx context.Context, c *db.Cluster, b *db.Backup, parentID string,
) (*batchv1.Job, error) {
	jobName := "backup-" + string(b.UID)
	cfg := instance.BackupConfig{Host: c.Status.ReadWriteService, RepositoryPath: b.Status.RepositoryPath, ParentID: parentID,
		Label: string(b.UID), Verify: ptr.Deref(b.Spec.Verify, true), Keys: c.Spec.KeysSecret != "", ChecksumKey: c.Spec.ChecksumKey}
	data, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	spec := basePod(c, r.OperatorImage, r.OperatorImagePullPolicy)
	spec.RestartPolicy = corev1.RestartPolicyNever
	container := baseContainer(c)
	container.Name = "backup"
	container.Command = []string{"/agent/manager", "instance", "backup"}
	container.Env = []corev1.EnvVar{{Name: "BACKUP_CONFIG", Value: string(data)}}
	addKeys(c, &spec, &container)
	spec.Volumes = append(spec.Volumes, claimVolume("backup", b.Status.ClaimName))
	container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: "backup", MountPath: instance.BackupDir})
	spec.Containers = []corev1.Container{container}
	deadline := b.Spec.DeadlineSeconds
	if deadline == 0 {
		deadline = 3600
	}
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: b.Namespace, Labels: labels(c)},
		Spec: batchv1.JobSpec{BackoffLimit: ptr.To(int32(0)), ActiveDeadlineSeconds: &deadline,
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"database.turborel.com/backup": string(b.UID)}}, Spec: spec}}}
	if err := controllerutil.SetControllerReference(b, job, r.Scheme); err != nil {
		return nil, err
	}
	if err := r.Create(ctx, job); err != nil {
		return nil, err
	}
	return job, nil
}

func (r *BackupReconciler) observeBackupJob(ctx context.Context, b *db.Backup, job *batchv1.Job) (ctrl.Result, error) {
	jobName := job.Name
	fail := func(reason, message string) (ctrl.Result, error) { return failBackup(b, reason, message) }
	for _, condition := range job.Status.Conditions {
		if condition.Status != corev1.ConditionTrue {
			continue
		}
		if condition.Type == batchv1.JobFailed {
			return fail("JobFailed", condition.Message)
		}
		if condition.Type == batchv1.JobComplete {
			pods := &corev1.PodList{}
			if err := r.List(ctx, pods, client.InNamespace(b.Namespace), client.MatchingLabels{"batch.kubernetes.io/job-name": jobName}); err != nil {
				return ctrl.Result{}, err
			}
			for _, pod := range pods.Items {
				if !metav1.IsControlledBy(&pod, job) {
					continue
				}
				for _, status := range pod.Status.ContainerStatuses {
					ended := status.State.Terminated
					if status.Name != "backup" || ended == nil || ended.ExitCode != 0 {
						continue
					}
					var output instance.BackupResult
					if err := json.Unmarshal([]byte(ended.Message), &output); err != nil {
						return fail("MissingResult", "Backup completed without a valid result")
					}
					if output.BackupID == "" || (ptr.Deref(b.Spec.Verify, true) && !output.Verified) {
						return fail("InvalidResult", "Backup result did not satisfy verification")
					}
					b.Status.BackupID = output.BackupID
					b.Status.Verified = output.Verified
					b.Status.Phase = "Completed"
					now := metav1.Now()
					b.Status.CompletedAt = &now
					meta.SetStatusCondition(&b.Status.Conditions, metav1.Condition{Type: "Complete", Status: metav1.ConditionTrue,
						Reason: "Completed", Message: "Backup completed", ObservedGeneration: b.Generation})
					return ctrl.Result{}, nil
				}
			}
			// Job and Pod watches can reach the cache in either order. Give the
			// successful container's termination message time to become visible.
			if job.Status.CompletionTime == nil || time.Since(job.Status.CompletionTime.Time) < time.Minute {
				return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
			}
			return fail("MissingResult", fmt.Sprintf("Completed Job %s has no successful backup result", jobName))
		}
	}
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}
func failBackup(b *db.Backup, reason, message string) (ctrl.Result, error) {
	b.Status.Phase = "Failed"
	meta.SetStatusCondition(&b.Status.Conditions, metav1.Condition{Type: "Complete", Status: metav1.ConditionFalse,
		Reason: reason, Message: message, ObservedGeneration: b.Generation})
	return ctrl.Result{}, nil
}
