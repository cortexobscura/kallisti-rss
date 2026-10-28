# Kubernetes

Kustomize manifests for one bot, one Kallisti server/account and one persistent
SQLite database. Supply your own image, configuration and existing credentials.
No Helm chart, image publication or cluster provisioning is included.

## Prerequisites and storage

- `kubectl` with Kustomize support, and a cluster permitting the supplied nonroot
  security context and resource limits.
- A CSI storage class supporting `ReadWriteOncePod`, filesystem ownership via
  `fsGroup`, and SQLite WAL/locking on a local filesystem backed by block storage.
  NFS/SMB and other shared network filesystems are unsupported. Set
  `storageClassName` in `pvc.yaml` if the default class is unsuitable.
- One replica only, with `Recreate` updates. Do not add autoscaling or force-delete
  pods while the old process might still run. `ReadWriteOncePod` restricts the PVC
  to one pod; `ReadWriteOnce` alone only restricts it to one node. See the
  [Kubernetes storage documentation](https://kubernetes.io/docs/concepts/storage/persistent-volumes/).

The PVC requests 1 GiB; size it for retained history and monitor usage. Check the
storage class reclaim policy and arrange backups before storing production state.
The namespace and PVC are deployment resources: deleting the namespace or running
`kubectl delete -k .` can destroy the database. Do not use those commands to stop
the bot.

## Configure and install

Build the root Dockerfile and make the image available to your cluster through
your own registry or local-cluster image loader. Set `images` in
`kustomization.yaml` to that image, preferably an immutable tag or digest. The
default `kallisti-rss:local` requires loading the image onto your cluster nodes;
it is not a public registry release. Configure `imagePullSecrets` in the pod spec
if your registry requires them.

From this directory:

```sh
cp config.example.yaml config.yaml
mkdir -p secrets
chmod 700 secrets
```

Edit `config.yaml` with your HTTPS server/Origin, existing account, channel store
IDs, verified manager IDs and feeds. Use a trusted editor to create
`secrets/password`, then `chmod 600 secrets/password`. These files are ignored by
Git. Configuration is a ConfigMap and must not contain credentials; the password
is mounted separately from a Secret. For session-token mode, replace
`password_file` with `token_file: /run/secrets/password` and store the token in the
same Secret key. Exactly one authentication mode must be configured.

Validate configuration with the native binary from the repository root:

```sh
make build
./bin/kallisti-rss -config deploy/kubernetes/config.yaml -check-config
```

Then, from this directory, run these operator commands against the intended
cluster context:

```sh
kubectl apply -f namespace.yaml
kubectl -n kallisti-rss create secret generic kallisti-rss-credentials \
  --from-file=password=secrets/password
kubectl apply --dry-run=server -k .
kubectl apply -k .
kubectl -n kallisti-rss rollout status deployment/kallisti-rss
kubectl -n kallisti-rss logs deployment/kallisti-rss --tail=100 -f
```

The Secret must exist before startup; credentials are never checked into a
manifest. Use your existing secret-management system for rotation, then restart
the Deployment so cached password sessions are replaced. Kustomize hashes the
ConfigMap name so applying changed configuration replaces the pod. Old generated
ConfigMaps may remain and can be removed after confirming they are unreferenced.

No Service, Ingress or HTTP probes are supplied because the bot exposes no
production listener or health endpoint. Pod readiness only establishes that the
process started, not server authentication, healthy feeds or successful delivery.
Inspect logs and complete the [live acceptance procedure](../../README.md#before-considering-a-deployment-verified).

The NetworkPolicy denies inbound traffic when enforced by the cluster CNI. It
does not restrict egress: DNS, Kallisti HTTPS/WSS and public feed/media hosts must
be reachable. Clusters with existing egress restrictions need appropriate allow
rules; dynamic public feeds cannot be captured by a fixed destination list here.
The application's public-only feed policy still applies. Node-level PID and log
retention limits are cluster responsibilities.

## Stop, update and recover

For a backup or offline repair, scale down and wait for the old pod to terminate:

```sh
kubectl -n kallisti-rss scale deployment/kallisti-rss --replicas=0
kubectl -n kallisti-rss wait --for=delete pod \
  -l app.kubernetes.io/name=kallisti-rss --timeout=120s
```

If termination times out, investigate before mounting the PVC elsewhere. Back up
the whole stopped data filesystem, including WAL/SHM, using your storage system's
snapshot/backup tooling. Preserve config, credentials and server/account identity.
For offline CLI repair, mount the PVC into a single maintenance pod with the same
image, config and UID/GID while the Deployment remains stopped; consult
[OPERATIONS.md](../../OPERATIONS.md) before resolving any job. Do not use database
inspection commands as probes: they take the exclusive lock and may create state.

After backup, update the image/configuration and `kubectl apply -k .` to restore
the configured single replica. Recreate updates intentionally cause downtime.
Restoring an older database requires reconciling uncertain remote deliveries
before reconnecting. Pausing/removing a subscription does not cancel queued posts.

These manifests require cluster-specific validation of storage, permissions,
network policy and live interoperability. Offline rendering and schema checks
do not certify a cluster deployment.
