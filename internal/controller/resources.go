package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	db "github.com/turborel/turborel-operator/api/v1alpha1"
	"github.com/turborel/turborel-operator/internal/instance"
)

const (
	clusterLabel       = "database.turborel.com/cluster"
	uidLabel           = "database.turborel.com/cluster-uid"
	roleLabel          = "database.turborel.com/role"
	revisionAnnotation = "database.turborel.com/revision"
)

func labels(c *db.Cluster) map[string]string {
	return map[string]string{clusterLabel: c.Name, uidLabel: string(c.UID), "app.kubernetes.io/name": "turborel",
		"app.kubernetes.io/managed-by": "turborel-operator"}
}
func memberName(c *db.Cluster, n int32) string { return fmt.Sprintf("%s-%d", c.Name, n) }
func memberDNS(c *db.Cluster, name string) string {
	return fmt.Sprintf("%s.%s-instances.%s.svc", name, c.Name, c.Namespace)
}
func serviceDNS(c *db.Cluster, suffix string) string {
	return c.Name + suffix + "." + c.Namespace + ".svc"
}
func memberConfig(c *db.Cluster, n int32) instance.Config {
	settings := maps.Clone(c.Spec.Settings)
	if settings == nil {
		settings = map[string]string{}
	}
	cfg := instance.Config{Member: n, Settings: settings, Bootstrap: !c.Status.Initialized,
		Keys: c.Spec.KeysSecret != "", ChecksumKey: c.Spec.ChecksumKey, Restore: c.Spec.Restore}
	if c.Spec.Instances > 1 {
		members := make([]string, 0, c.Spec.Instances)
		for i := int32(1); i <= c.Spec.Instances; i++ {
			members = append(members, fmt.Sprintf("%d=%s:7423", i, memberDNS(c, memberName(c, i))))
		}
		cfg.Members = strings.Join(members, ",")
	}
	// Paths are identical on each member; the repository is explicitly shared storage.
	settings["ARCHIVE.MODE"] = "OFF"
	settings["ARCHIVE.TARGET"] = ""
	if c.Spec.Backup != nil {
		settings["BACKUP.COMPRESSION"] = "ON"
		if c.Spec.Backup.Compression != nil && !*c.Spec.Backup.Compression {
			settings["BACKUP.COMPRESSION"] = "OFF"
		}
		if c.Spec.Backup.Archive {
			settings["ARCHIVE.MODE"] = "ON"
			settings["ARCHIVE.TARGET"] = instance.BackupDir + "/" + string(c.UID) + "/archive"
		}
	}
	return cfg
}
func digest(value any) string {
	// All callers supply JSON-compatible Kubernetes structs or string maps.
	data, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("hash internal resource: %v", err))
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func secretVolume(name, secret string) corev1.Volume {
	return corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
		SecretName: secret, DefaultMode: ptr.To(int32(0440))}}}
}
func claimVolume(name, claim string) corev1.Volume {
	return corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim}}}
}
func securityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{AllowPrivilegeEscalation: ptr.To(false), ReadOnlyRootFilesystem: ptr.To(true),
		Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}}
}
func podSecurityContext() *corev1.PodSecurityContext {
	return &corev1.PodSecurityContext{RunAsNonRoot: ptr.To(true), RunAsUser: ptr.To(int64(65532)), RunAsGroup: ptr.To(int64(65532)),
		FSGroup: ptr.To(int64(65532)), FSGroupChangePolicy: ptr.To(corev1.FSGroupChangeOnRootMismatch),
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}}
}
func basePod(c *db.Cluster, operatorImage string) corev1.PodSpec {
	return corev1.PodSpec{
		AutomountServiceAccountToken: ptr.To(false), SecurityContext: podSecurityContext(),
		TerminationGracePeriodSeconds: ptr.To(int64(60)), ImagePullSecrets: c.Spec.ImagePullSecrets,
		NodeSelector: c.Spec.NodeSelector, Tolerations: c.Spec.Tolerations,
		Volumes: []corev1.Volume{
			{Name: "agent", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			{Name: "run", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			secretVolume("tls", c.Name+"-tls"), secretVolume("owner", c.Spec.OwnerSecret),
		},
		InitContainers: []corev1.Container{{Name: "install-agent", Image: operatorImage, ImagePullPolicy: corev1.PullIfNotPresent,
			Command: []string{"/manager", "instance", "install", "/agent/manager"}, SecurityContext: securityContext(),
			Resources:    corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: quantity("10m"), corev1.ResourceMemory: quantity("32Mi")}},
			VolumeMounts: []corev1.VolumeMount{{Name: "agent", MountPath: "/agent"}}}},
	}
}
func baseContainer(c *db.Cluster) corev1.Container {
	container := corev1.Container{Name: "database", Image: c.Spec.Image, ImagePullPolicy: corev1.PullIfNotPresent,
		Command: []string{"/agent/manager", "instance", "run"}, SecurityContext: securityContext(), Resources: c.Spec.Resources,
		VolumeMounts: []corev1.VolumeMount{{Name: "agent", MountPath: "/agent", ReadOnly: true}, {Name: "run", MountPath: "/run/turborel"},
			{Name: "tmp", MountPath: "/tmp"}, {Name: "tls", MountPath: instance.TLSDir, ReadOnly: true},
			{Name: "owner", MountPath: instance.OwnerDir, ReadOnly: true}}}
	return container
}
func addKeys(c *db.Cluster, spec *corev1.PodSpec, container *corev1.Container) {
	if c.Spec.KeysSecret != "" {
		spec.Volumes = append(spec.Volumes, secretVolume("keys", c.Spec.KeysSecret))
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: "keys", MountPath: instance.KeysDir, ReadOnly: true})
	}
}
func desiredPod(c *db.Cluster, n int32, operatorImage, secretHash string) *corev1.Pod {
	name := memberName(c, n)
	spec := basePod(c, operatorImage)
	spec.Hostname = name
	spec.Subdomain = c.Name + "-instances"
	spec.Volumes = append(spec.Volumes, claimVolume("data", name), corev1.Volume{Name: "config",
		VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: name + "-config"}}}})
	container := baseContainer(c)
	container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: "data", MountPath: instance.DataRoot},
		corev1.VolumeMount{Name: "config", MountPath: "/etc/turborel/config", ReadOnly: true})
	container.Ports = []corev1.ContainerPort{{Name: "sql", ContainerPort: 7422}, {Name: "replication", ContainerPort: 7423}, {Name: "status", ContainerPort: 8000}}
	probe := corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"/agent/manager", "instance", "probe"}}},
		TimeoutSeconds: 3, PeriodSeconds: 5, FailureThreshold: 3}
	container.ReadinessProbe = probe.DeepCopy()
	// Database recovery can take arbitrarily long. Readiness, not a liveness restart loop, gates traffic.
	addKeys(c, &spec, &container)
	if c.Spec.Backup != nil {
		spec.Volumes = append(spec.Volumes, claimVolume("backup", c.Spec.Backup.ClaimName))
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: "backup", MountPath: instance.BackupDir})
	}
	if c.Spec.Restore != nil {
		spec.Volumes = append(spec.Volumes, claimVolume("restore", c.Spec.Restore.ClaimName))
		// PITR writes timeline history to its source archive, so this must be writable.
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: "restore", MountPath: instance.RestoreDir})
	}
	spec.Containers = []corev1.Container{container}
	spec.Affinity = c.Spec.Affinity.DeepCopy()
	if spec.Affinity == nil && c.Spec.Instances > 1 {
		spec.Affinity = &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{TopologyKey: "kubernetes.io/hostname",
				LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{uidLabel: string(c.UID)}}}}}}
	}
	cfg := memberConfig(c, n)
	cfg.Bootstrap = false
	revision := digest(struct {
		Spec    corev1.PodSpec
		Config  instance.Config
		Secrets string
	}{Spec: spec, Config: cfg, Secrets: secretHash})
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: c.Namespace, Labels: labels(c),
		Annotations: map[string]string{revisionAnnotation: revision}}, Spec: spec}
}
func desiredService(c *db.Cluster, suffix, role string) *corev1.Service {
	selector := map[string]string{uidLabel: string(c.UID)}
	if role != "" {
		selector[roleLabel] = role
	}
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: c.Name + suffix, Namespace: c.Namespace, Labels: labels(c)},
		Spec: corev1.ServiceSpec{Selector: selector, Ports: []corev1.ServicePort{{Name: "sql", Port: 7422, TargetPort: intstr.FromString("sql")}}}}
	if suffix == "-instances" {
		service.Spec.ClusterIP = corev1.ClusterIPNone
		service.Spec.PublishNotReadyAddresses = true
		service.Spec.Ports = append(service.Spec.Ports, corev1.ServicePort{Name: "replication", Port: 7423, TargetPort: intstr.FromString("replication")})
	}
	return service
}

func quantity(value string) resource.Quantity { return resource.MustParse(value) }
