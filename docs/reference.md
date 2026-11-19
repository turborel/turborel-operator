# API reference

All three resources are namespaced and use
`apiVersion: database.turborel.com/v1alpha1`. Secret, PVC and resource
references are resolved in that namespace. Use `kubectl explain
cluster.spec --api-version=database.turborel.com/v1alpha1 --recursive`
for the installed schema.

## Cluster

Names are DNS labels without dots, at most 40 characters. Short name:
`trc`.

| Spec field | Default | Meaning |
|---|---|---|
| `image` | required | image containing both Turborel binaries; pin a tested build |
| `instances` | `1` | `1`, `3` or `5`; immutable |
| `ownerSecret` | required | Secret with `username` and `password`; reference immutable |
| `storage.size` | required | positive Kubernetes quantity; may grow, never shrink |
| `storage.storageClassName` | cluster default | optional storage class; immutable, including presence |
| `settings` | `{}` | uppercase setting names with string values; see operations guide |
| `resources` | none | database container requests and limits |
| `keysSecret` | none | Secret containing the encryption key files |
| `checksumKey` | none | key file to register for HMAC-SHA-256 checksums |
| `backup.claimName` | none | existing shared RWX PVC |
| `backup.compression` | `true` | Turborel backup compression |
| `backup.archive` | `false` | continuous archiving; standalone only |
| `restore` | none | initialize a new standalone database from a backup; immutable |
| `nodeSelector`, `tolerations` | none | standard Kubernetes scheduling fields |
| `affinity` | separate nodes for replicas | override with Kubernetes affinity; `{}` explicitly disables the default |
| `imagePullSecrets` | `[]` | registry Secrets for database, instance-manager init container and backup Jobs |
| `maintenance` | `false` | pause controlled updates for changed configuration or images |

Restore fields:

| Field | Meaning |
|---|---|
| `claimName` | source PVC, required |
| `backupPath` | repository path relative to that PVC, required |
| `backupID` | exact backup ID; omitted selects the repository's newest backup |
| `archivePath` | optional archive path relative to the PVC |
| `untilTime` | UTC time as `YYYY-MM-DD HH:MM:SS` |
| `untilLSN` | positive log position |
| `untilPoint` | named restore point |

Choose at most one recovery target, and supply `archivePath` for any
target. Relative paths use slash-separated letters, digits, underscores
and hyphens. No traversal or absolute paths are accepted.

Status reports the observed primary, ready instance count, service
names, initialized flag and original PVC UIDs. `Ready` requires every
member to serve SQL and agree on membership, epoch and view; replicas
must have heard the primary recently. SQL readiness alone is not
permission to roll another member. `Updating` reports configuration or
image changes waiting for those checks and active backups to finish.

## Backup

Short name: `trb`. The spec is immutable. Create a new resource to retry
a failure.

| Spec field | Default | Meaning |
|---|---|---|
| `clusterName` | required | source Cluster |
| `parent` | none | completed Backup in the same Cluster and repository; makes this incremental |
| `verify` | `true` | verify the exact resulting backup ID before completion |
| `deadlineSeconds` | `3600` | Job lifetime, from 60 to 604800 seconds |

Phases are `Pending`, `Running`, `Completed` and `Failed`. Success sets
`Complete=True` and records `backupID`, `verified`, `claimName`,
`repositoryPath`, `clusterUID`, `jobName` and timestamps. Failure sets
`Complete=False` with a reason. Keep the backup's status with the files
and encryption keys needed for recovery.

## ScheduledBackup

Short name: `trsb`.

| Spec field | Default | Meaning |
|---|---|---|
| `clusterName` | required | source Cluster |
| `everySeconds` | `86400` | interval from 60 to 31536000 seconds, anchored at creation |
| `suspend` | `false` | stop creating Backups; an existing Job continues |
| `verify` | `true` | verification requested on each generated full Backup |

The first backup is due one interval after creation. A missed interval
causes one catch-up backup, and the next run waits for the previous one
to reach a terminal phase. Status names the last Backup and the last
and next schedule times. Deleting the schedule keeps its Backup records.
