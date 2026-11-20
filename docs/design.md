# Design

The operator uses Turborel's native replication and backup operations.
This first API keeps the replica group fixed and lets the database's
consensus protocol choose its primary.

## Pod management

Each member has independent storage, a process manager and separate
services for reads and writes. Updates replace replicas before the
primary.

The operator manages Pods directly. A StatefulSet's automatic rollout
would make update decisions outside the controller's checks for
healthy members and active backups. A stable Pod name and retained PVC
provide the member identity without delegating that rollout.

Turborel's consensus protocol chooses the primary. Kubernetes
leader-election Leases coordinate the operator's controllers; they
are not part of database consensus.

The initial API does not implement online membership changes,
controlled switchovers, cross-cluster replica
topologies, connection pooling, object-store providers, snapshot
backups, automatic retention or declarative SQL users and databases.
Those need their own protocols and tests before they can be added.

## Startup and replacement

A Cluster first records the UIDs of its volumes in status. Its Pods
are created only after that write succeeds. Later reconciliation
refuses a different PVC at the same name, or a missing one. PVCs and
the CA have no garbage-collection owner reference, and carry the
Cluster UID so a new Cluster cannot adopt them by name accidentally.

The instance manager writes a durable marker before initial replica
bootstrap. An empty replica directory after that point can only join
an existing group. A standalone database or completed restore writes
an initialization marker too; losing its data directory then fails
closed rather than creating a fresh database or restoring an old
backup over the loss.

If an initial group fails before it durably establishes its log and
owner, it can require manual recovery. Automatically deciding that an
empty directory means a new group would risk creating divergent data.

## Health, routing and updates

The instance manager checks SQL every two seconds. Its readiness file
expires after fifteen seconds. The controller reads member status over
mutual TLS and changes the role labels selected by the services. It
removes obsolete primary labels before assigning a new one. If more
than one member reports itself primary, the write service has no
selected primary until the observations agree.

Service updates and existing client connections are not instantaneous
fences. Turborel's consensus protocol must reject writes at a member
that cannot commit them. Clients must handle a disconnected session
or a read-only response and reconnect.

Updates require all members to pass SQL checks, agree on membership,
epoch and view, and have live primary links, with one observed primary
and no pending or running backup. One Pod is replaced, then the
controller waits for health again. It does not claim zero downtime or
prove application-level replica freshness from a TCP connection.
After a graceful database exit the process manager keeps the Pod alive
for five seconds so peers can observe the closed replication sockets
before Kubernetes removes its network. A failed update stops progress; maintenance mode prevents automatic
replacement for changed settings or images.

## Backups

A full backup starts a repository directory named by the Cluster and
Backup UIDs. An incremental backup reuses that directory and supplies
the exact parent ID. Different full chains cannot collide by label or
by directory. The Job records the resulting ID and verification result
in its termination message; the controller requires both to mark it
complete.

Jobs have no automatic retries. Losing a client after a backup commits
can leave an ambiguous outcome; silently retrying would create another
backup. The database's backup history and repository can be inspected
before a new Backup is requested.

Continuous archiving is restricted to standalone clusters in this
version. A replicated database needs a defined ownership and timeline
policy for archive writing across elections before that feature can be
claimed. Recovery is restricted to a new standalone database for the
same reason: restoring a file set is not a replicated-group bootstrap
protocol.

## Packaging

The Go types, validation markers and controllers live here. Operator
SDK supplied the Go project and controller-runtime supplies the
reconciliation framework. The installation is a Helm chart in
`turborel/charts`, with generated CRDs and a small Deployment and RBAC
surface. `make manifests` writes temporary test artifacts;
`make chart` updates the sibling chart's CRDs. There is no second
Kustomize installation tree to keep in sync.
