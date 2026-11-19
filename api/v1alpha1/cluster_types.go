package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ClusterSpec describes a Turborel database and its fixed replica group.
// +kubebuilder:validation:XValidation:rule="has(self.restore) == has(oldSelf.restore)",message="restore cannot be added or removed"
// +kubebuilder:validation:XValidation:rule="!has(self.restore) || self.instances == 1",message="restore currently requires one instance"
type ClusterSpec struct {
	// Image includes both turboreld and turborel. Pin a digest for reproducible upgrades.
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`
	// Instances is fixed for the lifetime of the group; Turborel membership changes require an epoch transition.
	// +kubebuilder:default=1
	// +kubebuilder:validation:Enum=1;3;5
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="instances is immutable; membership changes are not yet automated"
	Instances int32       `json:"instances,omitempty"`
	Storage   StorageSpec `json:"storage"`
	// OwnerSecret names a Secret containing username and password. It is also needed when restoring.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="ownerSecret is immutable"
	OwnerSecret string `json:"ownerSecret"`
	// Settings are Turborel settings applied at startup. Operator-owned networking and replication settings are reserved.
	// +kubebuilder:validation:MaxProperties=128
	// +kubebuilder:validation:XValidation:rule="self.all(k, k.matches('^[A-Z][A-Z0-9_]*[.][A-Z][A-Z0-9_]*$'))",message="use uppercase SECTION.SETTING names"
	// +kubebuilder:validation:XValidation:rule="self.all(k, !k.startsWith('REPLICATION.') && !k.startsWith('SERVER.') && !k.startsWith('ARCHIVE.') && !k.startsWith('JOBS.') && !k.startsWith('PLUGINS.'))",message="networking, replication, archive, jobs and plugins are operator-owned"
	Settings  map[string]string           `json:"settings,omitempty"`
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
	// KeysSecret holds encryption keys as files containing 64 hex digits. Keys must be retained with backups.
	// +kubebuilder:validation:MinLength=1
	KeysSecret string `json:"keysSecret,omitempty"`
	// ChecksumKey names a file in keysSecret for HMAC-SHA-256 checksums.
	// +kubebuilder:validation:Pattern=`^[A-Za-z][A-Za-z0-9_]*$`
	ChecksumKey string             `json:"checksumKey,omitempty"`
	Backup      *BackupStorageSpec `json:"backup,omitempty"`
	// Restore runs once, before serving a new standalone database.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="restore is immutable"
	Restore          *RestoreSpec                  `json:"restore,omitempty"`
	NodeSelector     map[string]string             `json:"nodeSelector,omitempty"`
	Tolerations      []corev1.Toleration           `json:"tolerations,omitempty"`
	Affinity         *corev1.Affinity              `json:"affinity,omitempty"`
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`
	// Maintenance pauses automatic pod replacement for configuration and image updates.
	Maintenance bool `json:"maintenance,omitempty"`
}

// StorageSpec fixes the storage class and allows capacity increases.
// +kubebuilder:validation:XValidation:rule="has(self.storageClassName) == has(oldSelf.storageClassName)",message="storageClassName cannot be added or removed"
type StorageSpec struct {
	// Size can grow when the storage class supports expansion; shrinking is rejected by the controller.
	Size resource.Quantity `json:"size"`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="storageClassName is immutable"
	StorageClassName *string `json:"storageClassName,omitempty"`
}

type BackupStorageSpec struct {
	// ClaimName is an existing ReadWriteMany PVC, mounted by all members and verification jobs.
	// +kubebuilder:validation:MinLength=1
	ClaimName string `json:"claimName"`
	// Archive enables Turborel's continuous log archive. Currently supported for standalone clusters only.
	Archive bool `json:"archive,omitempty"`
	// +kubebuilder:default=true
	Compression *bool `json:"compression,omitempty"`
}

// RestoreSpec restores a backup; archive recovery writes new timeline history to the source.
// +kubebuilder:validation:XValidation:rule="(has(self.untilTime) ? 1 : 0) + (has(self.untilLSN) ? 1 : 0) + (has(self.untilPoint) ? 1 : 0) <= 1",message="choose at most one recovery target"
// +kubebuilder:validation:XValidation:rule="(!has(self.untilTime) && !has(self.untilLSN) && !has(self.untilPoint)) || has(self.archivePath)",message="a recovery target requires archivePath"
type RestoreSpec struct {
	// ClaimName contains the backup repository and optional archive.
	// +kubebuilder:validation:MinLength=1
	ClaimName string `json:"claimName"`
	// BackupPath is a relative path within the source PVC.
	// +kubebuilder:validation:Pattern=`^[a-zA-Z0-9_-]+(/[a-zA-Z0-9_-]+)*$`
	BackupPath string `json:"backupPath"`
	// BackupID selects a specific backup. Omit to restore the newest.
	BackupID string `json:"backupID,omitempty"`
	// +kubebuilder:validation:Pattern=`^[a-zA-Z0-9_-]+(/[a-zA-Z0-9_-]+)*$`
	ArchivePath string `json:"archivePath,omitempty"`
	// UntilTime is a UTC timestamp in Turborel's YYYY-MM-DD HH:MM:SS format.
	// +kubebuilder:validation:Pattern=`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$`
	UntilTime string `json:"untilTime,omitempty"`
	// +kubebuilder:validation:Minimum=1
	UntilLSN   *int64 `json:"untilLSN,omitempty"`
	UntilPoint string `json:"untilPoint,omitempty"`
}

type MemberStatus struct {
	Name string `json:"name"`
	// VolumeUID binds bootstrap authorization to the original PVC.
	VolumeUID string `json:"volumeUID"`
	Ready     bool   `json:"ready"`
	Role      string `json:"role,omitempty"`
}

type ClusterStatus struct {
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Initialized permanently closes initial bootstrap after all members have served SQL.
	Initialized      bool   `json:"initialized,omitempty"`
	ReadyInstances   int32  `json:"readyInstances,omitempty"`
	Primary          string `json:"primary,omitempty"`
	ReadWriteService string `json:"readWriteService,omitempty"`
	ReadOnlyService  string `json:"readOnlyService,omitempty"`
	// +listType=map
	// +listMapKey=name
	Members []MemberStatus `json:"members,omitempty"`
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=trc
// +kubebuilder:printcolumn:name="Instances",type=integer,JSONPath=`.spec.instances`
// +kubebuilder:printcolumn:name="Ready",type=integer,JSONPath=`.status.readyInstances`
// +kubebuilder:printcolumn:name="Primary",type=string,JSONPath=`.status.primary`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="size(self.metadata.name) <= 40",message="cluster names must be at most 40 characters"
// +kubebuilder:validation:XValidation:rule="self.metadata.name.matches('^[a-z0-9]([-a-z0-9]*[a-z0-9])?$')",message="cluster names must be DNS labels without dots"
// Cluster is a Turborel database with persistent, independently stored members.
type Cluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ClusterSpec   `json:"spec"`
	Status            ClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type ClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Cluster `json:"items"`
}

func init() { SchemeBuilder.Register(&Cluster{}, &ClusterList{}) }
