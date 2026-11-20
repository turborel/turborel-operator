package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestImagePullPolicies(t *testing.T) {
	for _, tc := range []struct {
		name, image                       string
		override, wantDatabase, wantAgent corev1.PullPolicy
	}{
		{name: "nightly refreshes", image: "ghcr.io/turborel/turborel:nightly", wantDatabase: corev1.PullAlways, wantAgent: corev1.PullAlways},
		{name: "latest refreshes", image: "ghcr.io/turborel/turborel:latest", wantDatabase: corev1.PullAlways, wantAgent: corev1.PullAlways},
		{name: "commit tags can be cached", image: "ghcr.io/turborel/turborel:sha-test", wantDatabase: corev1.PullIfNotPresent, wantAgent: corev1.PullIfNotPresent},
		{name: "digest overrides mutable tag", image: "ghcr.io/turborel/turborel:nightly@sha256:1234", wantDatabase: corev1.PullIfNotPresent, wantAgent: corev1.PullIfNotPresent},
		{name: "kind agent stays local", image: "ghcr.io/turborel/turborel:nightly", override: corev1.PullIfNotPresent, wantDatabase: corev1.PullAlways, wantAgent: corev1.PullIfNotPresent},
		{name: "never pull agent", image: "ghcr.io/turborel/turborel:nightly", override: corev1.PullNever, wantDatabase: corev1.PullAlways, wantAgent: corev1.PullNever},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cluster := testCluster()
			cluster.Spec.Image = tc.image
			pod := desiredPod(cluster, 1, tc.image, "test-secrets", tc.override)
			if got := pod.Spec.Containers[0].ImagePullPolicy; got != tc.wantDatabase {
				t.Fatalf("database pull policy = %s, want %s", got, tc.wantDatabase)
			}
			if got := pod.Spec.InitContainers[0].ImagePullPolicy; got != tc.wantAgent {
				t.Fatalf("agent pull policy = %s, want %s", got, tc.wantAgent)
			}
		})
	}
}
