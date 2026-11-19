package controller

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	db "github.com/turborel/turborel-operator/api/v1alpha1"
	"github.com/turborel/turborel-operator/internal/instance"
	"github.com/turborel/turborel-operator/internal/pki"
)

// ClusterReconciler manages storage, identities and Pods; Turborel owns elections.
type ClusterReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	OperatorImage string
	// Inspect is injectable for deterministic reconciliation tests.
	Inspect func(context.Context, *db.Cluster, *corev1.Pod, map[string][]byte) (instance.Status, error)
}

func (r *ClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, retErr error) {
	c := &db.Cluster{}
	if err := r.Get(ctx, req.NamespacedName, c); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !c.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	before := c.DeepCopy()
	defer func() {
		if retErr != nil {
			setCondition(c, "Ready", false, "ReconcileFailed", retErr.Error())
		}
		if !reflect.DeepEqual(before.Status, c.Status) {
			if err := r.Status().Patch(ctx, c, client.MergeFrom(before)); err != nil && retErr == nil {
				retErr = err
			}
		}
	}()
	if err := validateCluster(c); err != nil {
		setCondition(c, "Ready", false, "InvalidSpec", err.Error())
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	c.Status.ObservedGeneration = c.Generation
	c.Status.ReadWriteService = serviceDNS(c, "-rw")
	c.Status.ReadOnlyService = serviceDNS(c, "-ro")
	tlsData, secretsHash, err := r.ensureSecrets(ctx, c)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := r.validateBackupStorage(ctx, c); err != nil {
		return ctrl.Result{}, err
	}
	registered, err := r.registerVolumes(ctx, c)
	if err != nil {
		return ctrl.Result{}, err
	}
	// Persist volume identities before authorizing any database process to initialize them.
	if registered {
		setCondition(c, "Ready", false, "Initializing", "Storage identities registered")
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}
	for _, service := range []*corev1.Service{desiredService(c, "-instances", ""), desiredService(c, "-rw", "primary"),
		desiredService(c, "-ro", "replica"), desiredService(c, "-r", "")} {
		if err := r.ensureService(ctx, c, service); err != nil {
			return ctrl.Result{}, err
		}
	}
	if err := r.ensurePDB(ctx, c); err != nil {
		return ctrl.Result{}, err
	}
	pods, desired, states, err := r.observeMembers(ctx, c, tlsData, secretsHash)
	if err != nil {
		return ctrl.Result{}, err
	}
	ready, primary := summarizeMembers(states)
	if err := r.routeMembers(ctx, c, pods, states, primary); err != nil {
		return ctrl.Result{}, err
	}
	c.Status.ReadyInstances = ready
	c.Status.Primary = primary
	allReady := ready == c.Spec.Instances && primary != "" && membersAgree(states, c.Spec.Instances)
	if allReady {
		c.Status.Initialized = true
	}
	setCondition(c, "Ready", allReady, "Reconciling", fmt.Sprintf("%d/%d instances ready", ready, c.Spec.Instances))
	if allReady {
		setCondition(c, "Ready", true, "Available", "All instances are serving SQL and agree on the replica view")
	}
	return r.updateMembers(ctx, c, pods, desired)
}

func setCondition(c *db.Cluster, name string, ok bool, reason, message string) {
	status := metav1.ConditionFalse
	if ok {
		status = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&c.Status.Conditions, metav1.Condition{Type: name, Status: status, Reason: reason,
		Message: message, ObservedGeneration: c.Generation})
}
func validateCluster(c *db.Cluster) error {
	if c.Spec.Instances != 1 && c.Spec.Instances != 3 && c.Spec.Instances != 5 {
		return fmt.Errorf("instances must be 1, 3 or 5")
	}
	if c.Spec.Image == "" || c.Spec.OwnerSecret == "" {
		return fmt.Errorf("image and ownerSecret are required")
	}
	if len(c.Name) > 40 || c.Spec.Storage.Size.Sign() <= 0 {
		return fmt.Errorf("name must be at most 40 characters and storage.size must be positive")
	}
	if c.Spec.Restore != nil && c.Spec.Instances != 1 {
		return fmt.Errorf("restore currently requires a standalone cluster")
	}
	if c.Spec.Backup != nil && c.Spec.Backup.Archive && c.Spec.Instances != 1 {
		return fmt.Errorf("continuous archiving currently requires a standalone cluster")
	}
	if c.Spec.ChecksumKey != "" && c.Spec.KeysSecret == "" {
		return fmt.Errorf("checksumKey requires keysSecret")
	}
	pattern := regexp.MustCompile(`^[A-Z][A-Z0-9_]*\.[A-Z][A-Z0-9_]*$`)
	for key := range c.Spec.Settings {
		reserved := strings.HasPrefix(key, "SERVER.") || strings.HasPrefix(key, "REPLICATION.") || strings.HasPrefix(key, "ARCHIVE.")
		if !pattern.MatchString(key) || reserved || strings.HasPrefix(key, "JOBS.") || strings.HasPrefix(key, "PLUGINS.") {
			return fmt.Errorf("setting %s is invalid or operator-owned", key)
		}
	}
	if len(memberConfig(c, 1).Members) > 1024 {
		return fmt.Errorf("replica addresses exceed Turborel's 1024-byte membership limit; shorten cluster or namespace names")
	}
	return nil
}

func inspectInstance(ctx context.Context, c *db.Cluster, pod *corev1.Pod, data map[string][]byte) (status instance.Status, retErr error) {
	config, err := pki.TLSConfig(data, memberDNS(c, pod.Name))
	if err != nil {
		return instance.Status{}, err
	}
	transport := &http.Transport{TLSClientConfig: config, Proxy: nil, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	httpClient := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+net.JoinHostPort(pod.Status.PodIP, "8000")+"/status", nil)
	if err != nil {
		return instance.Status{}, err
	}
	res, err := httpClient.Do(req)
	if err != nil {
		return instance.Status{}, err
	}
	defer func() { retErr = errors.Join(retErr, res.Body.Close()) }()
	if res.StatusCode != http.StatusOK {
		return instance.Status{}, fmt.Errorf("instance returned status %d", res.StatusCode)
	}
	err = json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&status)
	return status, err
}

func (r *ClusterReconciler) ensureSecrets(ctx context.Context, c *db.Cluster) (map[string][]byte, string, error) {
	hashes, err := r.credentialHashes(ctx, c)
	if err != nil {
		return nil, "", err
	}
	ca := &corev1.Secret{}
	key := types.NamespacedName{Namespace: c.Namespace, Name: c.Name + "-ca"}
	err = r.Get(ctx, key, ca)
	if apierrors.IsNotFound(err) {
		if c.Status.Initialized || len(c.Status.Members) > 0 {
			return nil, "", fmt.Errorf("cluster CA is missing; restore the retained CA Secret")
		}
		data, err := pki.Authority(c.Name, time.Now())
		if err != nil {
			return nil, "", err
		}
		ca = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace, Labels: labels(c)}, Data: data}
		if err := r.Create(ctx, ca); err != nil {
			return nil, "", err
		}
	} else if err != nil {
		return nil, "", err
	}
	if ca.Labels[uidLabel] != string(c.UID) {
		return nil, "", fmt.Errorf("CA belongs to another cluster identity")
	}
	leaf := &corev1.Secret{}
	key.Name = c.Name + "-tls"
	err = r.Get(ctx, key, leaf)
	missing := apierrors.IsNotFound(err)
	if err != nil && !missing {
		return nil, "", err
	}
	if !missing && !metav1.IsControlledBy(leaf, c) {
		return nil, "", fmt.Errorf("TLS Secret is not controlled by this cluster")
	}
	if missing || pki.NeedsRenewal(leaf.Data, time.Now()) {
		names := []string{serviceDNS(c, "-rw"), serviceDNS(c, "-ro"), serviceDNS(c, "-r")}
		for n := int32(1); n <= c.Spec.Instances; n++ {
			names = append(names, memberDNS(c, memberName(c, n)))
		}
		data, err := pki.Issue(ca.Data, names, time.Now())
		if err != nil {
			return nil, "", err
		}
		if missing {
			leaf = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace, Labels: labels(c)}, Type: corev1.SecretTypeTLS, Data: data}
			if err := controllerutil.SetControllerReference(c, leaf, r.Scheme); err != nil {
				return nil, "", err
			}
			if err := r.Create(ctx, leaf); err != nil {
				return nil, "", err
			}
		} else {
			before := leaf.DeepCopy()
			leaf.Data = data
			if err := r.Patch(ctx, leaf, client.MergeFrom(before)); err != nil {
				return nil, "", err
			}
		}
	}
	hashes["tls"] = digest(leaf.Data)
	return leaf.Data, digest(hashes), nil
}

func (r *ClusterReconciler) ensureVolume(ctx context.Context, c *db.Cluster, name string, registered bool) (*corev1.PersistentVolumeClaim, error) {
	claim := &corev1.PersistentVolumeClaim{}
	err := r.Get(ctx, types.NamespacedName{Namespace: c.Namespace, Name: name}, claim)
	if apierrors.IsNotFound(err) {
		if registered {
			return nil, fmt.Errorf("retained volume %s is missing; automatic empty-volume replacement is disabled", name)
		}
		claim = &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: c.Namespace, Labels: labels(c)},
			Spec: corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				StorageClassName: c.Spec.Storage.StorageClassName, Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: c.Spec.Storage.Size}}}}
		return claim, r.Create(ctx, claim)
	}
	if err != nil {
		return nil, err
	}
	if claim.Labels[uidLabel] != string(c.UID) {
		return nil, fmt.Errorf("volume %s belongs to another cluster identity", name)
	}
	if !claim.DeletionTimestamp.IsZero() {
		return nil, fmt.Errorf("volume %s is being deleted", name)
	}
	current := claim.Spec.Resources.Requests[corev1.ResourceStorage]
	switch c.Spec.Storage.Size.Cmp(current) {
	case -1:
		return nil, fmt.Errorf("volume %s cannot shrink", name)
	case 1:
		before := claim.DeepCopy()
		claim.Spec.Resources.Requests[corev1.ResourceStorage] = c.Spec.Storage.Size
		if err := r.Patch(ctx, claim, client.MergeFrom(before)); err != nil {
			return nil, err
		}
	}
	return claim, nil
}
func (r *ClusterReconciler) ensureConfig(ctx context.Context, c *db.Cluster, name string, cfg instance.Config) error {
	value, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name + "-config", Namespace: c.Namespace}}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, cm, func() error {
		if cm.ResourceVersion != "" && !metav1.IsControlledBy(cm, c) {
			return fmt.Errorf("configuration %s is not controlled by this cluster", cm.Name)
		}
		if previous := cm.Data["config.json"]; previous != "" {
			var old instance.Config
			if err := json.Unmarshal([]byte(previous), &old); err != nil {
				return err
			}
			for _, setting := range []string{"STORAGE.PAGE_BYTES", "STORAGE.CHECKSUM", "LOG.SEGMENT_BYTES"} {
				if old.Settings[setting] != cfg.Settings[setting] {
					return fmt.Errorf("creation setting %s cannot change", setting)
				}
			}
		}
		cm.Labels = labels(c)
		cm.Data = map[string]string{"config.json": string(value)}
		return controllerutil.SetControllerReference(c, cm, r.Scheme)
	})
	return err
}
func (r *ClusterReconciler) ensureService(ctx context.Context, c *db.Cluster, want *corev1.Service) error {
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: want.Name, Namespace: want.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, service, func() error {
		if service.ResourceVersion != "" && !metav1.IsControlledBy(service, c) {
			return fmt.Errorf("service %s is not controlled by this cluster", service.Name)
		}
		service.Labels = want.Labels
		service.Spec.Selector = want.Spec.Selector
		service.Spec.Ports = want.Spec.Ports
		service.Spec.PublishNotReadyAddresses = want.Spec.PublishNotReadyAddresses
		if want.Spec.ClusterIP == corev1.ClusterIPNone {
			service.Spec.ClusterIP = corev1.ClusterIPNone
		}
		return controllerutil.SetControllerReference(c, service, r.Scheme)
	})
	return err
}
func (r *ClusterReconciler) ensurePDB(ctx context.Context, c *db.Cluster) error {
	pdb := &policyv1.PodDisruptionBudget{ObjectMeta: metav1.ObjectMeta{Name: c.Name, Namespace: c.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, pdb, func() error {
		if pdb.ResourceVersion != "" && !metav1.IsControlledBy(pdb, c) {
			return fmt.Errorf("disruption budget is not controlled by this cluster")
		}
		minimum := intstr.FromInt32(c.Spec.Instances/2 + 1)
		pdb.Labels = labels(c)
		pdb.Spec.MinAvailable = &minimum
		pdb.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{uidLabel: string(c.UID)}}
		return controllerutil.SetControllerReference(c, pdb, r.Scheme)
	})
	return err
}
func (r *ClusterReconciler) validateBackupStorage(ctx context.Context, c *db.Cluster) error {
	names := []string{}
	if c.Spec.Backup != nil {
		names = append(names, c.Spec.Backup.ClaimName)
	}
	if c.Spec.Restore != nil {
		names = append(names, c.Spec.Restore.ClaimName)
	}
	for _, name := range names {
		claim := &corev1.PersistentVolumeClaim{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: c.Namespace, Name: name}, claim); err != nil {
			return err
		}
		if !slices.Contains(claim.Spec.AccessModes, corev1.ReadWriteMany) {
			return fmt.Errorf("backup/restore PVC %s must support ReadWriteMany", name)
		}
	}
	return nil
}
func (r *ClusterReconciler) backupActive(ctx context.Context, c *db.Cluster) (bool, error) {
	backups := &db.BackupList{}
	if err := r.List(ctx, backups, client.InNamespace(c.Namespace)); err != nil {
		return false, err
	}
	for _, b := range backups.Items {
		if b.Spec.ClusterName == c.Name && b.Status.Phase != "Completed" && b.Status.Phase != "Failed" {
			return true, nil
		}
	}
	return false, nil
}
func (r *ClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&db.Cluster{}).Owns(&corev1.Pod{}).Owns(&corev1.Service{}).
		Owns(&corev1.ConfigMap{}).Owns(&corev1.Secret{}).Owns(&policyv1.PodDisruptionBudget{}).Complete(r)
}

func (r *ClusterReconciler) registerVolumes(ctx context.Context, c *db.Cluster) (bool, error) {
	registered := false
	for n := int32(1); n <= c.Spec.Instances; n++ {
		name := memberName(c, n)
		index := slices.IndexFunc(c.Status.Members, func(m db.MemberStatus) bool { return m.Name == name })
		volume, err := r.ensureVolume(ctx, c, name, index >= 0)
		if err != nil {
			return false, err
		}
		if index < 0 {
			c.Status.Members = append(c.Status.Members, db.MemberStatus{Name: name, VolumeUID: string(volume.UID)})
			registered = true
			continue
		}
		if c.Status.Members[index].VolumeUID != string(volume.UID) {
			return false, fmt.Errorf("volume %s was replaced; refusing bootstrap on a different volume", name)
		}
	}
	return registered, nil
}

func (r *ClusterReconciler) observeMembers(
	ctx context.Context, c *db.Cluster, tlsData map[string][]byte, secretsHash string,
) ([]*corev1.Pod, map[string]*corev1.Pod, map[string]instance.Status, error) {
	pods := make([]*corev1.Pod, 0, c.Spec.Instances)
	desired := map[string]*corev1.Pod{}
	states := map[string]instance.Status{}
	for n := int32(1); n <= c.Spec.Instances; n++ {
		name := memberName(c, n)
		cfg := memberConfig(c, n)
		if err := r.ensureConfig(ctx, c, name, cfg); err != nil {
			return nil, nil, nil, err
		}
		want := desiredPod(c, n, r.OperatorImage, secretsHash)
		if err := controllerutil.SetControllerReference(c, want, r.Scheme); err != nil {
			return nil, nil, nil, err
		}
		desired[name] = want
		pod := &corev1.Pod{}
		err := r.Get(ctx, types.NamespacedName{Namespace: c.Namespace, Name: name}, pod)
		if apierrors.IsNotFound(err) {
			if err := r.Create(ctx, want); err != nil {
				return nil, nil, nil, err
			}
			pod = want
		} else if err != nil {
			return nil, nil, nil, err
		}
		if !metav1.IsControlledBy(pod, c) {
			return nil, nil, nil, fmt.Errorf("pod %s is not controlled by this cluster", name)
		}
		pods = append(pods, pod)
		state := instance.Status{}
		if pod.Status.PodIP != "" && pod.DeletionTimestamp.IsZero() && pod.Status.Phase == corev1.PodRunning {
			inspect := r.Inspect
			if inspect == nil {
				inspect = inspectInstance
			}
			observed, err := inspect(ctx, c, pod, tlsData)
			if err == nil && time.Since(observed.CheckedAt) < 15*time.Second {
				state = observed
			}
		}
		states[name] = state
	}
	return pods, desired, states, nil
}

func summarizeMembers(states map[string]instance.Status) (int32, string) {
	var ready int32
	primary := ""
	count := 0
	for name, state := range states {
		if !state.Ready {
			continue
		}
		ready++
		if state.Role == "primary" {
			primary = name
			count++
		}
	}
	if count != 1 {
		primary = ""
	}
	return ready, primary
}

func (r *ClusterReconciler) routeMembers(
	ctx context.Context, c *db.Cluster, pods []*corev1.Pod, states map[string]instance.Status, primary string,
) error {
	// Clear stale primary labels before assigning the observed primary. Database
	// consensus still fences writes during the bounded endpoint propagation delay.
	for _, pod := range pods {
		state := states[pod.Name]
		role := ""
		if state.Ready && state.Role == "replica" {
			role = "replica"
		}
		if pod.Name == primary {
			role = "primary"
		}
		if pod.Labels[roleLabel] != "" && pod.Labels[roleLabel] != role {
			previous := pod.DeepCopy()
			delete(pod.Labels, roleLabel)
			if err := r.Patch(ctx, pod, client.MergeFrom(previous)); err != nil {
				return err
			}
		}
	}
	for _, pod := range pods {
		state := states[pod.Name]
		role := ""
		if state.Ready && state.Role == "replica" {
			role = "replica"
		}
		if pod.Name == primary {
			role = "primary"
		}
		if role != "" && pod.Labels[roleLabel] != role {
			previous := pod.DeepCopy()
			if pod.Labels == nil {
				pod.Labels = map[string]string{}
			}
			pod.Labels[roleLabel] = role
			if err := r.Patch(ctx, pod, client.MergeFrom(previous)); err != nil {
				return err
			}
		}
		for i := range c.Status.Members {
			if c.Status.Members[i].Name == pod.Name {
				c.Status.Members[i].Ready = state.Ready
				c.Status.Members[i].Role = role
			}
		}
	}
	return nil
}

func (r *ClusterReconciler) updateMembers(
	ctx context.Context, c *db.Cluster, pods []*corev1.Pod, desired map[string]*corev1.Pod,
) (ctrl.Result, error) {
	allReady := meta.IsStatusConditionTrue(c.Status.Conditions, "Ready")
	primary := c.Status.Primary
	// A terminal Pod never restarts itself; recreate it against the same retained volume.
	for _, pod := range pods {
		if pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded {
			if err := r.Delete(ctx, pod); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
	}
	pending := false
	for _, pod := range pods {
		if pod.Annotations[revisionAnnotation] != desired[pod.Name].Annotations[revisionAnnotation] {
			pending = true
		}
	}
	setCondition(c, "Updating", pending, "UpToDate", "Instances use the desired configuration")
	if pending {
		setCondition(c, "Updating", true, "WaitingForHealthyCluster", "Updates require every instance healthy and no active backup")
		active, err := r.backupActive(ctx, c)
		if err != nil {
			return ctrl.Result{}, err
		}
		if allReady && !c.Spec.Maintenance && !active {
			// Replicas first. Deleting the primary last lets Turborel elect its successor.
			slices.SortStableFunc(pods, func(a, b *corev1.Pod) int {
				if a.Name == primary {
					return 1
				}
				if b.Name == primary {
					return -1
				}
				return strings.Compare(a.Name, b.Name)
			})
			for _, pod := range pods {
				if !pod.DeletionTimestamp.IsZero() {
					break
				}
				if pod.Annotations[revisionAnnotation] != desired[pod.Name].Annotations[revisionAnnotation] {
					if err := r.Delete(ctx, pod); err != nil {
						return ctrl.Result{}, err
					}
					setCondition(c, "Updating", true, "RollingUpdate", "Replacing "+pod.Name)
					break
				}
			}
		}
	}
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

func (r *ClusterReconciler) credentialHashes(ctx context.Context, c *db.Cluster) (map[string]string, error) {
	owner := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: c.Namespace, Name: c.Spec.OwnerSecret}, owner); err != nil {
		return nil, err
	}
	user := string(owner.Data["username"])
	password := string(owner.Data["password"])
	if !regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,62}$`).MatchString(user) || password == "" || strings.ContainsAny(password, "\r\n\x00") {
		return nil, fmt.Errorf("owner Secret needs a simple SQL username and a nonempty single-line password")
	}
	hashes := map[string]string{"owner": digest(owner.Data)}
	if c.Spec.KeysSecret != "" {
		keys := &corev1.Secret{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: c.Namespace, Name: c.Spec.KeysSecret}, keys); err != nil {
			return nil, err
		}
		for name, value := range keys.Data {
			decoded, err := hex.DecodeString(strings.TrimSpace(string(value)))
			if err != nil || len(decoded) != 32 {
				return nil, fmt.Errorf("key %s must contain 64 hexadecimal digits", name)
			}
		}
		if c.Spec.ChecksumKey != "" && len(keys.Data[c.Spec.ChecksumKey]) == 0 {
			return nil, fmt.Errorf("checksum key is absent from keysSecret")
		}
		hashes["keys"] = digest(keys.Data)
	}
	return hashes, nil
}

// A SQL-readable replica can still be behind or in another view. Require fresh
// primary links and agreement before replacing another member during a rollout.
func membersAgree(states map[string]instance.Status, expected int32) bool {
	var first *instance.Status
	for _, state := range states {
		if !state.Healthy || state.Members != expected {
			return false
		}
		if first == nil {
			first = &state
			continue
		}
		if state.Epoch != first.Epoch || state.View != first.View {
			return false
		}
	}
	return first != nil
}
