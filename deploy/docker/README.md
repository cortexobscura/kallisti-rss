# Docker Compose

Run one bot for one Kallisti server/account with persistent SQLite state. The
repository-root Dockerfile builds the image; this directory contains its Compose
setup. Docker Engine with Compose v2 is required. No published image is assumed.

The service entrypoint is `/feedbot -config /config/config.yaml`, so arguments
given to `docker compose run feedbot ...` are action flags appended to that
mounted configuration. Pass only the action flags shown below. A plain
`docker run` of the image does not use this entrypoint and needs its own
`-config /config/config.yaml`.

## Configure

From this directory:

```sh
cp config.example.yaml config.yaml
mkdir -p secrets data backups
chmod 700 secrets data backups
# Optional image-name override:
cp .env.example .env
```

Edit `config.yaml` with the server, Origin, existing account, text-channel store
IDs, manager registered IDs and public feeds. Keep the container paths
(`/run/secrets/password`, `/data/feedbot.db`) unchanged. Create `secrets/password`
using a trusted editor with the existing account password. No account is created
by the bot. For token authentication, replace `password_file` with `token_file`
using the same `/run/secrets/password` mounted path and put the session token in
that file; the filename does not select the authentication mode.

### File ownership

The container runs as UID/GID 65532. On a conventional (rootful) Linux Docker
host, after creating the files, grant that identity access:

```sh
sudo chown 65532:65532 data config.yaml secrets/password
sudo chmod 700 data
sudo chmod 600 config.yaml secrets/password
```

Later edits to `config.yaml` or the secret then need `sudo`. Missing bind sources
fail instead of silently becoming directories. Local config, secrets, data,
backups, `.env` and `compose.override.yaml` are ignored by Git and excluded from
the image build context.

Rootless Docker maps container UID 65532 to a subordinate host UID, so a host
`chown 65532` is wrong there. Set ownership from inside the user namespace with
the built image instead (this also works on rootful hosts):

```sh
docker compose build
docker run --rm --user 0 --network=none --entrypoint chown \
  -v "$PWD/data:/data" -v "$PWD/config.yaml:/c/config.yaml" \
  -v "$PWD/secrets:/c/secrets" kallisti-rss:local -R 65532:65532 /data /c
```

SELinux hosts need mount labels. Create a local `compose.override.yaml`; Compose
merges it automatically:

```yaml
services:
  feedbot:
    volumes:
      - {type: bind, source: ./config.yaml, target: /config/config.yaml, read_only: true, bind: {selinux: Z}}
      - {type: bind, source: ./secrets/password, target: /run/secrets/password, read_only: true, bind: {selinux: Z}}
      - {type: bind, source: ./data, target: /data, bind: {selinux: Z}}
```

Verify access on your host with the validation steps below before starting.

## Discover IDs

`channels` and `admin_ids` need Kallisti text-channel store IDs and registered
account IDs. With credentials and a config that has `server`, `origin` and
`username` set, discovery authenticates, prints JSON and exits without posting,
polling or opening the database (channels may still be empty here):

```sh
docker compose run --rm --no-deps -T feedbot -discover
```

Have intended managers online, verify them through your normal moderation
workflow, then set `admin_ids`. An empty list disables chat management. See the
[root README](../../README.md#configure-a-real-test-server) for details.

## Build, validate and start

```sh
docker compose config --quiet
docker compose build
docker compose run --rm --no-deps -T feedbot -check-config
docker compose run --rm --no-deps -T feedbot -check-feeds
docker run --rm --network=none --read-only --cap-drop=ALL \
  --security-opt=no-new-privileges:true --pids-limit=64 \
  --memory=512m --cpus=1 --tmpfs /tmp:size=32m,mode=1777 \
  kallisti-rss:local -demo
docker compose up -d
docker compose logs --tail=100 -f feedbot
```

`-check-config` reads no credentials and makes no network requests.
`-check-feeds` fetches and parses the YAML feeds only, without login, media or
database access. Use the selected image name in the demo command if `.env`
overrides it. The demo uses synthetic fixtures and no production mounts.

Starting the service connects to the configured server and feeds. The first
successful fetch establishes a baseline without posting backlog. No inbound ports
are exposed. Outbound DNS, HTTPS/WSS to Kallisti, and public feed and image access
are required; public HTTP requires the explicit config opt-in. Keep exactly one
instance per database. Do not run the root Compose setup against the same data
directory at the same time.

## Monitor

There is no health endpoint or Compose healthcheck. A running container is not
proof of successful authentication or delivery. Watch the logs:

| Log line | Meaning |
|---|---|
| `feed poll cycle completed` | Connected and polling; check its `failed` count |
| `feed poll failed` | One feed failed with a controlled `error_class`; others continue |
| `delivery acknowledged` | A post was acknowledged by the server and committed |
| `delivery quarantined` | A job is blocked for operator reconciliation |
| `connection unavailable; retrying in 30s` repeating | Login, token, TLS, network or channel visibility problem |
| `feedbot stopped` then a restart | Fatal config, database or identity error; `restart: unless-stopped` retries |

`docker compose ps` shows restart counts. After rotating the password or revoking
the session, restart the service (`docker compose restart feedbot`): a cached
password session is not replaced on every connection failure. Complete the
repository's [live acceptance procedure](../../README.md#before-considering-a-deployment-verified)
before treating a deployment as verified. Monitor disk use of `data/`;
deduplication history is retained indefinitely.

## Inspect, back up and update

Offline database commands take the exclusive lock, so stop the service first; they
refuse with `database already in use` while it runs. They can create a database
if the configured path is wrong, so check the mounted data first. Use `-T` when
redirecting output so no TTY is allocated:

```sh
docker compose stop feedbot
docker compose run --rm --no-deps -T feedbot -status
docker compose run --rm --no-deps -T feedbot -subscriptions
docker compose run --rm --no-deps -T feedbot -outbox > backups/unresolved.json
```

`-outbox` output contains original payloads, image URLs and upload keys: keep it
private. Reconcile and resolve jobs or pause subscriptions only as described in
[OPERATIONS.md](../../OPERATIONS.md), for example
`docker compose run --rm --no-deps -T feedbot -resolve-job 12 -resolution discarded`
or `... feedbot -pause-subscription 3 -expected-channel 42` after verifying the IDs.

Back up while stopped. Copy the entire data directory, including WAL/SHM files,
with the config; protect the archive like the credentials:

```sh
docker compose stop feedbot
sudo tar -czf "backups/feedbot-$(date +%Y%m%dT%H%M%S).tgz" data config.yaml
sudo chmod 600 backups/*.tgz
```

Store credentials through your secret manager rather than in the same archive
when possible. Keep the same server/account identity when restoring; an older
snapshot can reintroduce uncertain sends that need reconciliation.

To update, back up first, then:

```sh
docker compose build
docker compose run --rm --no-deps -T feedbot -check-config
docker compose run --rm --no-deps -T feedbot -status
docker compose up -d
```

`docker compose down` removes the containers/network, leaving bind-mounted data
intact. Never delete `data/` to repair delivery errors; changing YAML does not
erase queued work.

The root `compose.yaml` remains available for existing installs using
`config.container.yaml`, with the same entrypoint. Migrating here is optional:
stop the old service first, back up, and deliberately move or point the new
mounts at the existing data and credentials. Starting with an empty `data/` is a
new database, not a migration.

`make check-compose` exercises this Compose file with a prebuilt image,
synthetic config and networking disabled: config path, offline actions, database
ownership, the exclusive lock and SIGTERM shutdown. It does not test production
mounts, credentials or live connectivity.
