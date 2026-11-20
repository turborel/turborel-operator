#!/usr/bin/env bash
# Every Kubernetes write uses this suite's own kubeconfig and disposable cluster.
set -euo pipefail
cd "$(dirname "$0")/.."
export KIND_CLUSTER="${KIND_CLUSTER:-turborel-operator-e2e-$$}"
export TURBOREL_IMAGE="${TURBOREL_IMAGE:-ghcr.io/turborel/turborel:nightly}"
CHART_DIR="${CHART_DIR:-../charts/charts/turborel-operator}"
OPERATOR_TEST_IMAGE="turborel-operator:e2e-$$"
for tool in docker kind kubectl helm python3 rg; do command -v "$tool" >/dev/null; done
test -f "$CHART_DIR/Chart.yaml"
if kind get clusters | rg -qx "$KIND_CLUSTER"; then
  echo "Refusing to reuse existing cluster: $KIND_CLUSTER" >&2
  exit 1
fi
mkdir -p .test
TEST_RUN_DIR="$(mktemp -d "$PWD/.test/e2e.XXXXXX")"
export KUBECONFIG="$TEST_RUN_DIR/kubeconfig"
cleanup() {
  result=$?
  trap - EXIT
  if [[ "$result" != 0 ]]; then
    kind export logs "$TEST_RUN_DIR/logs" --name "$KIND_CLUSTER" || true
    kubectl get clusters,backups,scheduledbackups,pods,pvc -A -o yaml > "$TEST_RUN_DIR/resources.yaml" || true
  fi
  if [[ "${KEEP_CLUSTER:-0}" == 1 ]]; then
    echo "Kept $KIND_CLUSTER; KUBECONFIG=$KUBECONFIG"
  else
    kind delete cluster --name "$KIND_CLUSTER"
  fi
  exit "$result"
}
cat > "$TEST_RUN_DIR/kind.yaml" <<'YAML'
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
  - role: worker
  - role: worker
YAML
trap cleanup EXIT
kind create cluster --name "$KIND_CLUSTER" --config "$TEST_RUN_DIR/kind.yaml" --kubeconfig "$KUBECONFIG" --wait 120s
kubectl taint node "$KIND_CLUSTER-control-plane" node-role.kubernetes.io/control-plane-
kubectl -n kube-system rollout status daemonset/kube-proxy --timeout=120s
# Save only the local platform: pulling one architecture of a multiarch index
# leaves other index entries unavailable to kind's default image importer.
if [[ "$TURBOREL_IMAGE" == *:nightly ]]; then
  docker pull "$TURBOREL_IMAGE"
else
  docker image inspect "$TURBOREL_IMAGE" >/dev/null 2>&1 || docker pull "$TURBOREL_IMAGE"
fi
image_platform="$(docker image inspect --format '{{.Os}}/{{.Architecture}}' "$TURBOREL_IMAGE")"
docker image save --platform="$image_platform" -o "$TEST_RUN_DIR/database.tar" "$TURBOREL_IMAGE"
kind load image-archive "$TEST_RUN_DIR/database.tar" --name "$KIND_CLUSTER"
docker build -t "$OPERATOR_TEST_IMAGE" .
kind load docker-image "$OPERATOR_TEST_IMAGE" --name "$KIND_CLUSTER"
kubectl apply --server-side -f .test/crds/
helm install operator "$CHART_DIR" --namespace turborel-system --create-namespace \
  --set image.repository=turborel-operator --set image.tag="e2e-$$" --set image.pullPolicy=IfNotPresent --wait --timeout=120s
python3 hack/e2e.py
