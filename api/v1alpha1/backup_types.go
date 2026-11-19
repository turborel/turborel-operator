package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// BackupSpec describes one online backup. A unique repository directory makes retries and verification unambiguous.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="backup spec is immutable"
type BackupSpec struct {
	// +kubebuilder:validation:MinLength=1
	ClusterName string `json:"clusterName"`
	// Parent names a completed Backup in the same cluster, for an incremental chain.
	Parent string `json:"parent,omitempty"`
	// +kubebuilder:default=true
	Verify *bool `json:"verify,omitempty"`
	// +kubebuilder:default=3600
	// +kubebuilder:validation:Minimum=60
	// +kubebuilder:validation:Maximum=604800
	DeadlineSeconds int64 `json:"deadlineSeconds,omitempty"`
}

type BackupStatus struct {
	// +kubebuilder:validation:Enum=Pending;Running;Completed;Failed
	Phase    string `json:"phase,omitempty"`
	BackupID string `json:"backupID,omitempty"`
	Verified bool   `json:"verified,omitempty"`
	JobName  string `json:"jobName,omitempty"`
	// RepositoryPath is relative to the backup PVC and shared by an incremental chain.
	RepositoryPath string       `json:"repositoryPath,omitempty"`
	ClaimName      string       `json:"claimName,omitempty"`
	ClusterUID     string       `json:"clusterUID,omitempty"`
	StartedAt      *metav1.Time `json:"startedAt,omitempty"`
	CompletedAt    *metav1.Time `json:"completedAt,omitempty"`
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=trb
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=`.spec.clusterName`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// Backup runs an online backup and, by default, an offline verification Job.
type Backup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              BackupSpec   `json:"spec"`
	Status            BackupStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type BackupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Backup `json:"items"`
}

func init() { SchemeBuilder.Register(&Backup{}, &BackupList{}) }
