<p align="center">
    <picture>
      <source media="(prefers-color-scheme: dark)" srcset="artwork/turborel-logo-white.svg">
      <img src="artwork/turborel-logo.svg" alt="turborel" width="360">
    </picture>
</p>

> The operator is in alpha. Its API is `v1alpha1`, and Turborel's
> SQL, protocol and files may change between builds. Pin the database
> and operator images to builds you have tested together. This is not
> yet a production release.

The Turborel operator runs [Turborel](https://turborel.com/) databases
on Kubernetes. Give it a `Cluster`: it makes the volumes, certificates,
Pods and services, starts the database, and keeps them running.

A cluster has one server, or a group of three or five. Each has its own
volume. Turborel commits changes through a majority of the group and
chooses a new primary when one is lost. The operator follows that
choice, sends clients to the primary, and replaces failed Pods over
their existing volumes.

Written in Go with [Operator SDK](https://sdk.operatorframework.io/),
it uses three resources: `Cluster`, `Backup` and `ScheduledBackup`.
No admission webhook or certificate manager is required.

## What it has

- Standalone databases and fixed groups of three or five replicas,
  with stable member numbers and DNS names.
- A volume for each member, kept when its Pod or Cluster is deleted.
  Volumes may grow when their storage class allows it. A missing or
  replaced volume is reported; it is never silently made into a new
  database.
- TLS for clients, mutual TLS between replicas, and a private CA for
  each cluster. The operator renews server certificates before they
  expire. Users and passwords come from a Secret.
- Services for writes, reads from replicas, and reads from any member.
  Their routing follows the roles Turborel reports through SQL.
- SQL readiness checks, graceful shutdown, a disruption budget that
  protects a majority, and replicas placed on different worker nodes
  by default.
- Changes to an image, settings or mounted Secrets applied one member
  at a time, replicas before the primary. An unhealthy group pauses
  the update. Maintenance mode pauses automatic updates too.
- Turborel settings, including memory budgets, diagnostics, compression
  and encryption at rest. Encryption keys are mounted from a Secret.
- Online full and incremental backups to an existing shared PVC, with
  verification by `turboreld --verify-backup` before a Backup is marked
  complete. A schedule takes full backups at a chosen interval.
- Restore into a new standalone cluster, optionally through a log
  archive to a time, log position or named restore point. Continuous
  archiving is currently for standalone clusters.
- Status conditions, structured logs and the operator's Prometheus
  metrics. The database's diagnostics remain in its own logs.

## Quick start

You need Kubernetes 1.33 or later with a default storage class,
`kubectl` and Helm. Install the operator:

```sh
helm repo add turborel https://charts.turborel.com
helm repo update
helm install turborel-operator turborel/turborel-operator \
  --namespace turborel-system --create-namespace
```

Create the database owner's credentials, replacing `change-me` with
your password:

```sh
kubectl create namespace databases
kubectl -n databases create secret generic example-owner \
  --from-literal=username=admin --from-literal=password='change-me'
```

Save this as `cluster.yaml`:

```yaml
apiVersion: database.turborel.com/v1alpha1
kind: Cluster
metadata:
  name: example
  namespace: databases
spec:
  image: ghcr.io/turborel/turborel:nightly
  ownerSecret: example-owner
  storage:
    size: 10Gi
```

Start the database and wait for it to be ready:

```sh
kubectl apply -f cluster.yaml
kubectl -n databases wait cluster/example --for=condition=Ready --timeout=5m
```

This creates one server. For three replicas, add `instances: 3` before
creating the Cluster; the default placement needs three worker nodes.
The instance count is fixed after creation. See the
[API reference](docs/reference.md) for settings and other options.

## Connecting

Open the SQL shell in the example database:

```sh
kubectl -n databases exec -it example-1 -c database -- \
  turborel --socket /run/turborel/database.sock --user admin --password
```

Enter the password you chose above, then run SQL. Type `\q` to exit.
The client is included in the database image.

Applications connect to `example-rw.databases.svc:7422` with the
Turborel client. See [connections](docs/operations.md#connections) for
TLS, read services and access from your machine.

## Backups and restore

Give `spec.backup.claimName` the name of an existing `ReadWriteMany`
PVC in the cluster's namespace. Every member and verification Job
mounts it at `/backups`. The storage must permit UID/GID 65532 to
write there.

```yaml
spec:
  backup:
    claimName: example-backups
    compression: true
```

Take a full backup:

```yaml
apiVersion: database.turborel.com/v1alpha1
kind: Backup
metadata:
  name: example-full
  namespace: databases
spec:
  clusterName: example
```

For an incremental backup, add `parent: example-full` to a new Backup's
spec. The parent must have completed in the same cluster and
repository. Keep the whole chain: removing a full backup or an
intermediate backup can make those following it unrestorable.

```sh
kubectl -n databases wait backup/example-full --for=condition=Complete --timeout=1h
kubectl -n databases get backup/example-full -o yaml
```

The status gives the PVC, repository path and Turborel backup ID.
`Complete=True` means the backup and its requested verification
succeeded. `spec.verify: false` skips verification. A failed Job is
not retried automatically: inspect its logs, then create a new Backup.
Deleting a Backup removes its Job, but keeps the files on the PVC.

A schedule takes full backups; it waits for the previous run to finish
and takes only one after a missed interval:

```yaml
apiVersion: database.turborel.com/v1alpha1
kind: ScheduledBackup
metadata:
  name: example-daily
  namespace: databases
spec:
  clusterName: example
  everySeconds: 86400
```

To restore, create a new Cluster with `instances: 1` and `spec.restore`.
Use the completed Backup's `status.repositoryPath` and `status.backupID`:

```yaml
spec:
  image: ghcr.io/turborel/turborel:nightly
  instances: 1
  ownerSecret: example-owner
  storage:
    size: 10Gi
  restore:
    claimName: example-backups
    backupPath: CLUSTER_UID/backups/BACKUP_UID
    backupID: BACKUP_ID
```

The restored database keeps its original users and passwords. Give it
the corresponding owner Secret, and the original encryption keys if
it was encrypted. The source PVC is mounted at `/restore`. A restore
through an archive writes its new timeline history there.

For a standalone source, `spec.backup.archive: true` enables continuous
log archiving under `CLUSTER_UID/archive`. Add that relative path as
`restore.archivePath`, then optionally choose one of `untilTime`
(`YYYY-MM-DD HH:MM:SS`, UTC), `untilLSN`, or `untilPoint`. Without a
target, recovery follows the archive to its end.

There is no automatic backup retention or built-in S3 provider here.
See [operations](docs/operations.md) for storage, encryption and recovery
limits.

## Development

```sh
make build             # generate APIs and manifests, check, compile
make test              # unit tests and the race detector
make test-integration  # CRDs against a real local API server
make test-e2e          # a separate kind cluster; see docs/testing.md
```

The [testing guide](docs/testing.md) gives the image and tooling needed
for the end-to-end suite. To run the controller outside Kubernetes,
set `OPERATOR_IMAGE` to an image the cluster can pull, then run
`go run ./cmd`. `WATCH_NAMESPACE` limits the watch to one namespace;
an empty value watches all namespaces. The installed deployment uses
leader election and protects its metrics with Kubernetes authorization.

`make chart` copies the generated CRDs into the chart in `../charts`
and lints it. Helm installs CRDs once and keeps them on uninstall;
before upgrading, apply the new chart’s `crds/` directory with
`kubectl apply --server-side --force-conflicts -f ../charts/charts/turborel-operator/crds/`.
The operator repository has no separate Kustomize or OLM installation tree.

## Contributing

[CONTRIBUTING.md](CONTRIBUTING.md) describes checks and commit conventions.
A contribution is made under the license in [LICENSE](LICENSE), and
the first pull request from a contributor asks them to sign the
[Contributor License Agreement](CLA.md).

## License

The code is under the BSD-2-Clause Plus Patent License in
[LICENSE](LICENSE). The name Turborel and its logos are not; see
[TRADEMARK.md](https://github.com/turborel/turborel/blob/main/TRADEMARK.md).
