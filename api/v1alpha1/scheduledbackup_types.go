package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

type ScheduledBackupSpec struct {
	// +kubebuilder:validation:MinLength=1
	ClusterName string `json:"clusterName"`
	// EverySeconds is the interval between full backups, measured in UTC from creation.
	// +kubebuilder:default=86400
	// +kubebuilder:validation:Minimum=60
	// +kubebuilder:validation:Maximum=31536000
	EverySeconds int64 `json:"everySeconds,omitempty"`
	Suspend      bool  `json:"suspend,omitempty"`
	// +kubebuilder:default=true
	Verify *bool `json:"verify,omitempty"`
}
type ScheduledBackupStatus struct {
	LastBackup       string       `json:"lastBackup,omitempty"`
	LastScheduleTime *metav1.Time `json:"lastScheduleTime,omitempty"`
	NextScheduleTime *metav1.Time `json:"nextScheduleTime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=trsb
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=`.spec.clusterName`
// +kubebuilder:printcolumn:name="Suspended",type=boolean,JSONPath=`.spec.suspend`
// +kubebuilder:printcolumn:name="LastBackup",type=string,JSONPath=`.status.lastBackup`
// ScheduledBackup periodically creates full Backups, without overlapping runs.
type ScheduledBackup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ScheduledBackupSpec   `json:"spec"`
	Status            ScheduledBackupStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type ScheduledBackupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ScheduledBackup `json:"items"`
}

func init() { SchemeBuilder.Register(&ScheduledBackup{}, &ScheduledBackupList{}) }
