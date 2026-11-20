# The Turborel Operator Documentation Set

The Turborel operator runs [Turborel](https://turborel.com/) databases
on Kubernetes: given a `Cluster`, it makes the volumes, certificates,
Pods and services, starts the database, and keeps them running. These
guides describe it for those who install and run it, and for those who
change it. The database itself, its SQL and the settings a Cluster
passes to it are described in Turborel's own manuals,
[The Turborel Documentation Set](https://github.com/turborel/turborel/blob/main/doc/README.md).

The operator is in alpha: its API is `v1alpha1`, and what these guides
describe may change in any release until Turborel's 1.0.0. Pin the
database image and the operator's to builds tested together.

## The guides

| Guide | For | |
|---|---|---|
| [Operations](operations.md) | Whoever runs databases with the operator | Images and credentials, connections, storage and deletion, settings and encryption, certificates, backup storage and recovery, inspecting failures |
| [API Reference](reference.md) | The same, looking a field up | `Cluster`, `Backup` and `ScheduledBackup`, field by field |
| [Testing](testing.md) | Whoever tries the operator on one machine, or changes it | A local kind installation, the end-to-end suite, release checks |
| [Design](design.md) | Whoever changes the operator | Why Pods are managed directly, how members start and are replaced, health and updates, backups, packaging |

The repository's [README](../README.md) has the quick start: installing
the operator, a first Cluster, connecting, and a first backup and
restore.
