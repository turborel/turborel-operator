package controller

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	db "github.com/turborel/turborel-operator/api/v1alpha1"
)

type ScheduledBackupReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *ScheduledBackupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	schedule := &db.ScheduledBackup{}
	if err := r.Get(ctx, req.NamespacedName, schedule); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if schedule.Spec.Suspend || !schedule.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	interval := time.Duration(schedule.Spec.EverySeconds) * time.Second
	if interval < time.Minute {
		return ctrl.Result{}, fmt.Errorf("backup interval must be at least sixty seconds")
	}
	if schedule.Status.LastBackup != "" {
		last := &db.Backup{}
		err := r.Get(ctx, types.NamespacedName{Namespace: schedule.Namespace, Name: schedule.Status.LastBackup}, last)
		if err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		if err == nil && last.Status.Phase != "Completed" && last.Status.Phase != "Failed" {
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
	}
	now := time.Now().UTC()
	next := schedule.CreationTimestamp.Add(interval)
	if schedule.Status.LastScheduleTime != nil {
		next = schedule.Status.LastScheduleTime.Add(interval)
	}
	if next.After(now) {
		return ctrl.Result{RequeueAfter: next.Sub(now)}, nil
	}
	// Catch up once, using an anchored time slot. A restart before status persistence
	// creates the same name, so it cannot duplicate a backup.
	elapsed := now.Sub(schedule.CreationTimestamp.Time)
	slot := schedule.CreationTimestamp.Add((elapsed / interval) * interval)
	name := fmt.Sprintf("scheduled-%s-%d", string(schedule.UID)[:8], slot.Unix())
	backup := &db.Backup{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: schedule.Namespace,
		Labels: map[string]string{"database.turborel.com/schedule-uid": string(schedule.UID)}},
		Spec: db.BackupSpec{ClusterName: schedule.Spec.ClusterName, Verify: schedule.Spec.Verify, DeadlineSeconds: 3600}}
	// Backup records and their Jobs outlive the schedule, like the backup data.
	if err := r.Create(ctx, backup); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return ctrl.Result{}, err
		}
		if err := r.Get(ctx, client.ObjectKeyFromObject(backup), backup); err != nil {
			return ctrl.Result{}, err
		}
		if backup.Labels["database.turborel.com/schedule-uid"] != string(schedule.UID) {
			return ctrl.Result{}, fmt.Errorf("scheduled backup name is already in use")
		}
	}
	before := schedule.DeepCopy()
	schedule.Status.LastBackup = name
	schedule.Status.LastScheduleTime = &metav1.Time{Time: slot}
	schedule.Status.NextScheduleTime = &metav1.Time{Time: slot.Add(interval)}
	if err := r.Status().Patch(ctx, schedule, client.MergeFrom(before)); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: interval}, nil
}
func (r *ScheduledBackupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&db.ScheduledBackup{}).Complete(r)
}
