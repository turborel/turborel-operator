# Testing

The tests have three levels. Unit tests exercise reconciliation,
volume identity, routing, rollout gates, backup outcomes, scheduling
and certificates. Envtest installs the generated CRDs into a real
Kubernetes API server and tests defaults and admission rules. The kind
suite runs the published Turborel binaries and verifies committed rows.

```sh
make test
make test-integration
make test-e2e
```

## End-to-end setup

Install Docker, kind, kubectl, Helm, Python 3 and ripgrep. Keep the
charts repository beside this checkout, or set `CHART_DIR` to its
`charts/turborel-operator` directory. Run `make chart` after changing
the Go API so the chart and generated CRDs agree.

The suite creates a separate three-node kind cluster with a private
kubeconfig. It refuses to reuse an existing cluster and does not change
the default kubectl context. It builds and loads the operator image,
loads the database image and installs the Helm chart. The control-plane
node is made schedulable so all three replicas have separate nodes.

```sh
TURBOREL_IMAGE=ghcr.io/turborel/turborel:sha-3af6c384f44669b441e1c471bf7ac75705030d02 \
  make test-e2e
```

The image must contain `/usr/local/bin/turboreld` and
`/usr/local/bin/turborel`. Pull it with Docker first if its registry
requires authentication. The suite imports the local platform into
kind, so the test Pods do not need registry credentials.

A full run checks TLS connections, replica reads, writes after a
primary failure, member rejoin, rolling updates, full and incremental
backup verification, restore, archive recovery to a named point,
schedules, encryption with keyed checksums, and retained PVCs.
`E2E_SCENARIO=recovery` runs recovery checks alone;
`E2E_SCENARIO=replication` runs replication checks alone.

The backup fixture is a hostPath on one kind worker, with the source,
verification Jobs and restored databases pinned there. This tests the
backup protocol; it is not a qualification of any shared storage
provider. Production repositories need an actual shared filesystem.

The cluster is deleted when the suite exits. Set `KEEP_CLUSTER=1` to
keep it for inspection; the script prints its kubeconfig path. On
failure, node logs and resource state are saved under `.test/e2e.*/`.
Those files can contain test credentials and are ignored by Git.

If kind's kube-proxy fails with an inotify limit error on Docker
Desktop, raise the Docker VM's `fs.inotify.max_user_instances` limit
before retrying. The suite does not change host kernel limits.

## Release checks

Run the unit, API, lint, vulnerability and full real-image suites before
calling a build ready for release. Check both all-namespace and scoped
chart values, including multiple operator replicas and authenticated
metrics. Apply the chart's CRDs explicitly before a Helm upgrade; Helm
does not upgrade them automatically. When taking over fields installed
by Helm, review the new schemas and use
`kubectl apply --server-side --force-conflicts -f CHART/crds/`.

A passing test with one build does not establish rolling compatibility
between different Turborel builds. Test that upgrade and its recovery
path separately. The suite does not certify partition fencing, lost
storage recovery, backup retention, CA rotation, storage drivers or
long-running behavior under load.
