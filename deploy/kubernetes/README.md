# Kubernetes

Kustomize manifests for one bot, one Kallisti server/account and one persistent
SQLite database. Supply your own image, configuration and existing credentials.
No Helm chart, image publication or cluster provisioning is included.

## Prerequisites and storage

- `kubectl` with Kustomize support, and a cluster permitting the supplied nonroot
  security context and resource limits.
- A storage class providing a local filesystem with SQLite WAL/locking semantics
  and `fsGroup` ownership: node-local storage such as Rancher local-path or
  OpenEBS hostpath, or CSI block storage. NFS/SMB and other shared network
  filesystems are unsupported. The PVC names no class, so it uses the cluster
  default; set `storageClassName` in `pvc.yaml` when there is no default class or
  it is unsuitable, otherwise the claim stays `Pending`.
- One replica only, with `Recreate` updates. Do not add autoscaling or force-delete
  pods while the old process might still run. The PVC uses `ReadWriteOnce`, which
  every provisioner supports but which only limits the volume to one node; the
  single replica, `Recreate` and the database file lock keep one writer. On a CSI
  driver that supports it, `ReadWriteOncePod` additionally limits the claim to one
  pod; local-path and hostpath provisioners are not CSI and cannot provide it. With
  node-local storage the pod is pinned to the node holding the volume. See the
  [Kubernetes storage documentation](https://kubernetes.io/docs/concepts/storage/persistent-volumes/).
- The namespace enforces the `restricted` Pod Security Standard, which the pod
  already satisfies. Pin the `*-version` labels if your cluster policy requires it.

The PVC requests 1 GiB; size it for retained history and monitor usage. Check the
storage class reclaim policy and arrange backups before storing production state.
The namespace and PVC are deployment resources: deleting the namespace or running
`kubectl delete -k .` can destroy the database. Do not use those commands to stop
the bot.

## Configure and install

Build the root Dockerfile and make the image available to your cluster through
your own registry or local-cluster image loader. Set `images` in
`kustomization.yaml` to that image, preferably an immutable tag or digest. With
`imagePullPolicy: IfNotPresent`, reusing a tag such as `local` does not roll out a
rebuilt image, so give every build a unique tag or pin the digest:

```yaml
images:
  - name: kallisti-rss
    newName: registry.example.com/you/kallisti-rss
    newTag: git-abc1234
    digest: sha256:<digest from docker push>
```
 The
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

The NetworkPolicy denies inbound traffic when enforced by the cluster CNI. By
default it does not restrict egress: DNS, Kallisti HTTPS/WSS and public feed/media
hosts must be reachable. The optional `components/egress-public` component limits
egress to cluster DNS (`k8s-app: kube-dns` in `kube-system`) and public IPv4 on
TCP 80/443, excluding private, tailnet (100.64.0.0/10), link-local and reserved
ranges as a second layer behind the application's own public-only checks. Enable
it by adding to `kustomization.yaml`:

```yaml
components:
  - components/egress-public
```

Do not enable it when the Kallisti server or a feed is only reachable on a
private, tailnet or IPv6 address, or when your DNS pods carry different labels;
adjust a copy instead. Dynamic public feeds cannot be captured by a fixed
destination list.
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
