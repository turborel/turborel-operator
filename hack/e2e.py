#!/usr/bin/env python3
"""Exercise the installed operator in a disposable kind cluster."""
import json
import os
import secrets
import subprocess
import time
import uuid

NAMESPACE = os.environ.get("TEST_NAMESPACE", "turborel-e2e")
IMAGE = os.environ.get("TURBOREL_IMAGE", "ghcr.io/turborel/turborel:nightly")
KIND_CLUSTER = os.environ.get("KIND_CLUSTER", "turborel-operator-e2e")
API = "database.turborel.com/v1alpha1"
HA_NAME = os.environ.get("TEST_CLUSTER", "ha")


def kubectl(*args, data=None):
    result = subprocess.run(["kubectl", *args], input=data, text=True, capture_output=True)
    if result.returncode:
        raise RuntimeError(f"kubectl {' '.join(args)}: {result.stderr}")
    return result.stdout


def apply(obj):
    kubectl("apply", "-f", "-", data=json.dumps(obj))


def get(kind, name):
    return json.loads(kubectl("-n", NAMESPACE, "get", kind, name, "-o", "json"))


def wait(description, predicate, timeout=180):
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        try:
            value = predicate()
            if value:
                print(description, flush=True)
                return value
        except RuntimeError as error:
            last = error
        time.sleep(2)
    raise RuntimeError(f"Timed out: {description}; {last or 'condition not reached'}")


def resource(kind, name, spec):
    return {"apiVersion": API, "kind": kind, "metadata": {"name": name, "namespace": NAMESPACE}, "spec": spec}


def cluster_spec(instances):
    return {"image": IMAGE, "instances": instances, "ownerSecret": "owner", "storage": {"size": "1Gi"},
            "settings": {"MEMORY.BUDGET_BYTES": "134217728"},
            "resources": {"requests": {"cpu": "100m", "memory": "256Mi"}, "limits": {"memory": "512Mi"}}}


def ready(name):
    c = get("cluster", name)
    return c if any(x["type"] == "Ready" and x["status"] == "True" and
                    x.get("observedGeneration") == c["metadata"]["generation"]
                    for x in c.get("status", {}).get("conditions", [])) else None


def sql(cluster, statement, suffix="rw", attempts=3):
    name = "sql-" + uuid.uuid4().hex[:10]
    apply({"apiVersion": "v1", "kind": "Pod", "metadata": {"name": name, "namespace": NAMESPACE},
           "spec": {"restartPolicy": "Never", "automountServiceAccountToken": False,
                    "securityContext": {"runAsUser": 65532, "runAsGroup": 65532, "fsGroup": 65532},
                    "containers": [{"name": "client", "image": IMAGE, "imagePullPolicy": "IfNotPresent",
                                    "command": ["/usr/local/bin/turborel"],
                                    "args": ["--host", f"{cluster}-{suffix}.{NAMESPACE}.svc", "--tls", "/tls/ca.crt",
                                             "--user", "admin", "--quiet", "--unaligned", "--tuples-only",
                                             "--stop-on-error", "--connect-timeout", "10", "--read-timeout", "30",
                                             "--command", statement],
                                    "env": [{"name": "TURBOREL_PASSWORD", "valueFrom": {"secretKeyRef": {"name": "owner", "key": "password"}}}],
                                    "volumeMounts": [{"name": "tls", "mountPath": "/tls", "readOnly": True}]}],
                    "volumes": [{"name": "tls", "secret": {"secretName": cluster + "-tls", "items": [{"key": "ca.crt", "path": "ca.crt"}]}}]}})
    pod = wait("SQL client finished", lambda: (p if (p := get("pod", name))["status"]["phase"] in ("Succeeded", "Failed") else None))
    output = kubectl("-n", NAMESPACE, "logs", name).strip()
    kubectl("-n", NAMESPACE, "delete", "pod", name, "--interactive=false", "--wait=false")
    if pod["status"]["phase"] != "Succeeded":
        # Endpoint updates lag the observed election. Only retry a refused
        # connection, which cannot have executed any part of the statement.
        if attempts > 1 and "cannot connect: Connection refused" in output:
            time.sleep(2)
            return sql(cluster, statement, suffix, attempts - 1)
        raise RuntimeError(f"SQL failed: {output}")
    return output


def backup(name, parent=None, cluster="single"):
    spec = {"clusterName": cluster}
    if parent:
        spec["parent"] = parent
    apply(resource("Backup", name, spec))
    b = wait(name + " finished", lambda: (b if (b := get("backup", name)).get("status", {}).get("phase") in ("Completed", "Failed") else None))
    if b["status"]["phase"] != "Completed" or not b["status"].get("verified"):
        raise RuntimeError(f"Backup did not verify: {b.get('status')}")
    return b["status"]


def run():
    apply({"apiVersion": "v1", "kind": "Namespace", "metadata": {"name": NAMESPACE}})
    apply({"apiVersion": "v1", "kind": "Secret", "metadata": {"name": "owner", "namespace": NAMESPACE},
           "stringData": {"username": "admin", "password": "local-test-password"}})


def replication():
    apply(resource("Cluster", HA_NAME, cluster_spec(3)))
    wait("Three replicas ready", lambda: ready(HA_NAME))
    sql(HA_NAME, "CREATE SCHEMA AUTHORIZATION ADMIN CREATE TABLE CHECKS (ID INTEGER PRIMARY KEY, V INTEGER); INSERT INTO CHECKS VALUES (1, 42); COMMIT;")
    assert sql(HA_NAME, "SELECT V FROM CHECKS") == "42"
    assert sql(HA_NAME, "SELECT V FROM CHECKS", suffix="ro") == "42"
    old_primary = get("cluster", HA_NAME)["status"]["primary"]
    node = get("pod", old_primary)["spec"]["nodeName"]
    kubectl("taint", "node", node, "e2e.turborel.com/unavailable=true:NoSchedule", "--overwrite")
    try:
        kubectl("-n", NAMESPACE, "delete", "pod", old_primary, "--interactive=false", "--wait=false")
        wait("Native election routed a new primary", lambda: get("cluster", HA_NAME).get("status", {}).get("primary") not in (None, "", old_primary))
        assert sql(HA_NAME, "INSERT INTO CHECKS VALUES (2, 99); COMMIT; SELECT COUNT(*) FROM CHECKS") == "2"
    finally:
        kubectl("taint", "node", node, "e2e.turborel.com/unavailable-")
    wait("Original member rejoined", lambda: ready(HA_NAME))
    old_uids = {f"{HA_NAME}-{i}": get("pod", f"{HA_NAME}-{i}")["metadata"]["uid"] for i in range(1, 4)}
    kubectl("-n", NAMESPACE, "patch", "cluster", HA_NAME, "--type=merge", "-p", json.dumps({"spec": {"settings": {"DIAGNOSTICS.SLOW_STATEMENT_MS": "1000"}}}))
    wait("Every member updated over its volume", lambda: ready(HA_NAME) and all(get("pod", name)["metadata"]["uid"] != uid for name, uid in old_uids.items()))
    assert sql(HA_NAME, "SELECT COUNT(*) FROM CHECKS") == "2"
    print("PASS: TLS SQL, replica reads, failover, rejoin and rolling updates", flush=True)



def recovery():
    # A local hostPath is a fixture only. A real repository needs shared RWX storage.
    worker = KIND_CLUSTER + "-worker"
    subprocess.run(["docker", "exec", worker, "mkdir", "-p", "/var/turborel-e2e-backups"], check=True)
    subprocess.run(["docker", "exec", worker, "chown", "65532:65532", "/var/turborel-e2e-backups"], check=True)
    apply({"apiVersion": "v1", "kind": "PersistentVolume", "metadata": {"name": NAMESPACE + "-backups"},
           "spec": {"capacity": {"storage": "2Gi"}, "accessModes": ["ReadWriteMany"], "storageClassName": "",
                    "hostPath": {"path": "/var/turborel-e2e-backups", "type": "Directory"},
                    "claimRef": {"namespace": NAMESPACE, "name": "backups"}}})
    apply({"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": {"name": "backups", "namespace": NAMESPACE},
           "spec": {"storageClassName": "", "volumeName": NAMESPACE + "-backups", "accessModes": ["ReadWriteMany"], "resources": {"requests": {"storage": "2Gi"}}}})
    single = cluster_spec(1)
    single["nodeSelector"] = {"kubernetes.io/hostname": worker}
    single["backup"] = {"claimName": "backups", "archive": True}
    apply(resource("Cluster", "single", single))
    wait("Standalone ready", lambda: ready("single"))
    sql("single", "CREATE SCHEMA AUTHORIZATION ADMIN CREATE TABLE CHECKS (ID INTEGER PRIMARY KEY, V INTEGER); INSERT INTO CHECKS VALUES (1, 7); COMMIT;")
    full = backup("full")
    sql("single", "INSERT INTO CHECKS VALUES (2, 8); COMMIT;")
    incremental = backup("incremental", "full")
    assert full["repositoryPath"] == incremental["repositoryPath"]
    restore = cluster_spec(1)
    restore["nodeSelector"] = single["nodeSelector"]
    restore["restore"] = {"claimName": "backups", "backupPath": incremental["repositoryPath"], "backupID": incremental["backupID"]}
    apply(resource("Cluster", "restored", restore))
    wait("Incremental restore ready", lambda: ready("restored"))
    assert sql("restored", "SELECT COUNT(*) FROM CHECKS") == "2"
    sql("single", "INSERT INTO CHECKS VALUES (3, 9); COMMIT; CREATE RESTORE POINT before_more; INSERT INTO CHECKS VALUES (4, 10); COMMIT;")
    position = int(sql("single", "SELECT \"POSITION\" FROM INFORMATION_SCHEMA.RESTORE_POINTS WHERE POINT_NAME = 'BEFORE_MORE'"))
    wait("Restore point archived", lambda: int(sql("single", "SELECT COUNT(*) FROM INFORMATION_SCHEMA.ARCHIVED_SEGMENTS WHERE LAST_POSITION >= " + str(position))) > 0)
    pitr = dict(restore)
    pitr["restore"] = dict(restore["restore"], archivePath=incremental["clusterUID"] + "/archive", untilPoint="BEFORE_MORE")
    apply(resource("Cluster", "point-restored", pitr))
    wait("Point-in-time restore ready", lambda: ready("point-restored"))
    assert sql("point-restored", "SELECT COUNT(*) FROM CHECKS") == "3"
    # Schedules use fixed intervals; creation in the past tests restart catch-up in unit tests.
    apply(resource("ScheduledBackup", "frequent", {"clusterName": "single", "everySeconds": 60}))
    schedule = wait("Scheduled backup created", lambda: (s if (s := get("scheduledbackup", "frequent")).get("status", {}).get("lastBackup") else None), timeout=100)
    name = schedule["status"]["lastBackup"]
    wait("Scheduled backup verified", lambda: get("backup", name).get("status", {}).get("phase") == "Completed")
    kubectl("-n", NAMESPACE, "delete", "cluster", "restored", "--interactive=false", "--wait=false")
    wait("Cluster Pods removed", lambda: not json.loads(kubectl("-n", NAMESPACE, "get", "pods", "-l", "database.turborel.com/cluster=restored", "-o", "json"))["items"])
    assert get("pvc", "restored-1")["status"]["phase"] == "Bound"
    print("PASS: standalone, full and incremental backup, restore, schedule, retained storage", flush=True)


def encrypted_recovery():
    worker = KIND_CLUSTER + "-worker"
    apply({"apiVersion": "v1", "kind": "Secret", "metadata": {"name": "keys", "namespace": NAMESPACE},
           "stringData": {"MASTER": secrets.token_hex(32)}})
    encrypted = cluster_spec(1)
    encrypted["nodeSelector"] = {"kubernetes.io/hostname": worker}
    encrypted["keysSecret"] = "keys"
    encrypted["checksumKey"] = "MASTER"
    encrypted["settings"].update({"STORAGE.ENCODINGS": "LZ4,AES-256-GCM:MASTER", "STORAGE.CHECKSUM": "HMAC-SHA-256"})
    encrypted["backup"] = {"claimName": "backups"}
    apply(resource("Cluster", "encrypted", encrypted))
    wait("Encrypted database ready", lambda: ready("encrypted"))
    sql("encrypted", "CREATE SCHEMA AUTHORIZATION ADMIN CREATE TABLE CHECKS (ID INTEGER PRIMARY KEY); INSERT INTO CHECKS VALUES (1); COMMIT;")
    saved = backup("encrypted-full", cluster="encrypted")
    restored = dict(encrypted)
    del restored["backup"]
    restored["restore"] = {"claimName": "backups", "backupPath": saved["repositoryPath"], "backupID": saved["backupID"]}
    apply(resource("Cluster", "encrypted-restored", restored))
    wait("Encrypted restore ready", lambda: ready("encrypted-restored"))
    assert sql("encrypted-restored", "SELECT COUNT(*) FROM CHECKS") == "1"
    print("PASS: encrypted backup and restore with keyed checksums", flush=True)


if __name__ == "__main__":
    run()
    scenario = os.environ.get("E2E_SCENARIO", "all")
    if scenario in ("all", "replication"):
        replication()
    if scenario in ("all", "recovery"):
        recovery()
        encrypted_recovery()
