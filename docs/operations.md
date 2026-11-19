# Operations

## Images and credentials

Use an image containing both Turborel binaries, and keep its version
fixed across the group. The current examples use
`ghcr.io/turborel/turborel:sha-3af6c384f44669b441e1c471bf7ac75705030d02`.
The corresponding registry digest observed during testing is
`sha256:fac2c9e50658f3031fe82b32a165b37d7a682b91b43ce98aa80df89e02ae4d95`.

For a private registry, set `Cluster.spec.imagePullSecrets` to Secrets
in the database namespace. They apply to the database, instance-manager
init container and backup Jobs. The chart's `imagePullSecrets` applies
to the operator Deployment separately.

The owner Secret has `username` and `password` keys. The username must
be an unquoted SQL identifier, and the password a nonempty line without
its newline. Changing that Secret does not execute `ALTER USER` in the
database. Coordinate password changes through SQL; mismatched owner
credentials stop readiness and administrative operations. Prefer
application users with their own privileges to using the owner.

## Storage and deletion

A member's PVC has its Pod's name, such as `main-1`. Only increasing
`spec.storage.size` is supported, and the storage class must allow
expansion. Never remove a database PVC to request a restart: delete the
Pod and let the controller reuse its volume instead.

Deleting a Cluster stops its Pods and removes owned services, config
maps, TLS leaf Secret and disruption budget. It retains the data PVCs,
CA Secret and all user-supplied Secrets and backup PVCs. Namespace
deletion can still delete those namespaced resources, and the storage
class's reclaim policy determines what happens to the underlying disk.

A Cluster of the same name has a different UID and cannot adopt the old
volumes. Restore into a new Cluster, or recover the original Cluster's
identity and resources as an explicit administrative operation. The
operator does not provide automatic lost-volume replacement yet.

A standalone cluster has `minAvailable: 1` in its disruption budget,
so draining its node requires an explicit maintenance decision. A group
of three has `minAvailable: 2`, and five has `minAvailable: 3`.
The operator's own controlled updates delete Pods directly; they use
stricter health checks rather than depending on the eviction API.

## Settings and encryption

Settings are uppercase names and string values. They are passed as
individual `--set` arguments, never through a shell. The server owns
their validation. Networking, replication, archive, job and plug-in
settings are reserved because they can invalidate the operator's
lifecycle assumptions.

`STORAGE.PAGE_BYTES`, `STORAGE.CHECKSUM` and `LOG.SEGMENT_BYTES` are fixed
at database creation. The controller refuses a change. Removing other
settings from the spec does not reset their persisted database values;
set the desired value explicitly.

A key Secret contains one file per key, each holding 64 hexadecimal
digits. Set `keysSecret`, and use, for example:

```yaml
settings:
  STORAGE.ENCODINGS: LZ4,AES-256-GCM:MASTER
```

For keyed checksums, set `checksumKey: MASTER` and
`STORAGE.CHECKSUM: HMAC-SHA-256` before creation. Retain every key needed
by existing files and backup chains. The operator mounts and checks
keys; it does not decide when an old key is safe to discard. Turborel's
[encryption guide](https://turborel.com/docs/operator/encryption/)
describes rekeying and table resealing.

A Kubernetes memory limit covers more than Turborel's own memory
budget. Leave room for the server, instance manager, CLI probes and
native allocations. The operator does not derive a budget from the
container limit.

## Certificates

`NAME-ca` contains the cluster CA and signing key. `NAME-tls` contains
the public CA and the leaf certificate and key used by members. The
signing key is never mounted into database Pods. The CA is valid for
ten years; leaf certificates for ninety days, renewed with thirty days
remaining. Renewal causes an ordered Pod update.

The controller and members share the leaf identity in this initial
version. Client SQL authentication still uses SCRAM passwords; the
operator does not enable certificate-to-owner authentication. Automatic
CA rotation and externally supplied issuers are not implemented.

Keep the operator running through the renewal window. An operator
that was offline until certificates expired may be unable to verify
member health and will not bypass TLS to force a rollout. Restore valid
credentials and recover members under an explicit maintenance plan.

## Backup storage and recovery

Repositories must be existing `ReadWriteMany` PVCs accessible to all
members and verification Jobs. The data volumes remain independent
`ReadWriteOnce` claims. The operator does not create or manage a shared
filesystem, nor remove backup files automatically.

A Backup status names its claim, repository path, source Cluster UID,
Turborel backup ID and verification result. Keep those records with the
backup files and encryption keys. Deleting a Backup record removes its
Job but not its data; deleting a schedule keeps its previous Backup
records.

A restored cluster needs the owner's original credentials. A backup
may be encrypted even when the new spec contains no encoding setting;
its original keys are still required to read it. Verify recovery on a
new cluster before relying on a backup chain.

For point-in-time recovery, the source must have archived the needed
log records. The archive path is `CLUSTER_UID/archive`, while a backup
chain is under `CLUSTER_UID/backups/BACKUP_UID`. The restore source is
writable because Turborel records new timeline history there. A target
outside the archive's coverage must fail, not return a partial restore.

## Inspecting failures

```sh
kubectl get cluster main -o yaml
kubectl describe pod main-1
kubectl logs main-1 -c database
kubectl get backup main-full -o yaml
```

An image, volume, owner Secret or TLS failure prevents readiness.
`Ready` requires every requested member to serve SQL and agree on the
replica membership, epoch and view, with recent primary heartbeats at
the replicas. It becomes false when a member is unavailable or those
observations disagree, even if a majority can still commit. `Updating` reports a pending or active rollout.
The write service may remain available during that degraded state.

A failed backup Job can be inspected by the name in `status.jobName`.
Create a new Backup to retry; do not delete the failed Job and expect
the same Backup to run again. If the Job disappears during execution,
the controller records a failure rather than taking a second backup.
