# Docker Compose

Run one bot for one Kallisti server/account with persistent SQLite state. The
repository-root Dockerfile builds the image; this directory contains its Compose
setup. Docker Engine with Compose v2 is required. No published image is assumed.

## Configure

From this directory:

```sh
cp config.example.yaml config.yaml
mkdir -p secrets data
chmod 700 secrets data
# Optional image-name override:
cp .env.example .env
```

Edit `config.yaml` with the server, Origin, existing account, text-channel store
IDs, manager registered IDs and public feeds. Create `secrets/password` using a
trusted editor with the existing account password. No account is created by the
bot. For token authentication, replace `password_file` with `token_file` using
the same `/run/secrets/password` mounted path and put the session token in that
file; the filename does not select the authentication mode.

The container runs as UID/GID 65532. On a conventional Linux Docker host, after
creating the files, grant that identity access:

```sh
sudo chown 65532:65532 data config.yaml secrets/password
sudo chmod 700 data
sudo chmod 600 config.yaml secrets/password
```

Rootless Docker/user namespaces and SELinux hosts may need different host ID
mapping or mount labels. Verify access on your host. Missing bind sources fail
instead of silently becoming directories. Local config, secrets, data and `.env`
are ignored by Git and excluded from the image build context.

## Build, validate and start

```sh
docker compose config --quiet
docker compose build
docker compose run --rm --no-deps feedbot -check-config
docker run --rm --network=none --read-only --cap-drop=ALL \
  --security-opt=no-new-privileges:true --pids-limit=64 \
  --memory=512m --cpus=1 --tmpfs /tmp:size=32m,mode=1777 \
  kallisti-rss:local -demo
docker compose up -d
docker compose logs --tail=100 -f feedbot
```

Use the selected image name in the demo command if `.env` overrides it. The demo
uses synthetic fixtures and no production mounts. Starting the service connects
to the configured server and feeds. The first successful fetch establishes a
baseline without posting backlog. A running container is not proof of successful
authentication or delivery; inspect logs and follow the repository's
[live acceptance procedure](../../README.md#before-considering-a-deployment-verified).

No inbound ports are exposed. Outbound DNS, HTTPS/WSS to Kallisti, and public feed
and image access are required; public HTTP requires the explicit config opt-in.
Keep exactly one instance per database. Do not run the root Compose setup against
the same data directory at the same time.

## Update, inspect and recover

Before updating, stop the bot and back up the entire data directory, including
WAL/SHM files, along with config and securely stored credentials. Keep the same
server/account identity. See [OPERATIONS.md](../../OPERATIONS.md) for restoring
and reconciling uncertain sends; changing YAML does not erase queued work.

```sh
docker compose stop feedbot
# Take the stopped backup here, then update the source/image.
docker compose build
docker compose run --rm --no-deps feedbot -status
docker compose up -d
```

Offline database commands require the service to be stopped and can create a
database if the configured path is wrong. Check the mounted data before using
them. `docker compose down` removes the containers/network, leaving bind-mounted
data intact. Never delete `data/` to repair delivery errors. Monitor disk use;
deduplication history is retained indefinitely.

The root `compose.yaml` remains available for existing installs using
`config.container.yaml`. Migrating here is optional: stop the old service first,
back up, and deliberately move or point the new mounts at the existing data and
credentials. Starting with an empty `data/` is a new database, not a migration.
