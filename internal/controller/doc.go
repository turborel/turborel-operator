// Package controller reconciles Turborel database resources.
//
// +kubebuilder:rbac:groups=database.turborel.com,resources=scheduledbackups,verbs=get;list;watch
// +kubebuilder:rbac:groups=database.turborel.com,resources=scheduledbackups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=database.turborel.com,resources=clusters,verbs=get;list;watch
// +kubebuilder:rbac:groups=database.turborel.com,resources=clusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=pods;services;configmaps;secrets;persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=database.turborel.com,resources=backups,verbs=get;list;watch;create
// +kubebuilder:rbac:groups=database.turborel.com,resources=backups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;delete
package controller
