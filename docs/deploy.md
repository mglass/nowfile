# Deploy

How the nowfile image is built and how the production box runs it. CI builds and pushes the image; it never touches a server. Deploying to the box is done by hand by the coordinator with Mike.

## Image

`ghcr.io/mglass/nowfile`, built by `.github/workflows/ci.yml` on every push to `main` after gofmt, vet and tests pass. Tags: `latest` and `sha-<short commit>`. Deploy a `sha-` tag when you want to know exactly what is running. Images are linux/amd64 only.

The image is distroless (`gcr.io/distroless/static-debian12:nonroot`) and runs as uid 65532. It contains `/usr/local/bin/nowfile` and, once `cmd/plan` exists, `/usr/local/bin/plan`. There is no shell in the image.

## Ports

| Container | Serves | Host |
|---|---|---|
| 7979 | finger | `79`, public, firewall allows 79/tcp |
| 8080 | HTTP | `127.0.0.1:8091`, behind Caddy |

The process runs as nonroot and cannot bind 79, so the image sets `NOWFILE_FINGER_ADDR=:7979` and the host maps 79 to it. Finger is not behind Caddy. `8091` is a placeholder: check it against the ports other clients already use on the box before first run.

## Volume

`/data` holds the SQLite database (`/data/app.db`). On the box it is the bind mount `/srv/clients/nowfile/data`, backed up by Litestream like any client.

A bind mount keeps the host directory's ownership, so before first run:

```sh
sudo mkdir -p /srv/clients/nowfile/data
sudo chown 65532:65532 /srv/clients/nowfile/data
```

## Environment

| Variable | Default in the image | Notes |
|---|---|---|
| `NOWFILE_HTTP_ADDR` | `:8080` | |
| `NOWFILE_FINGER_ADDR` | `:7979` | set by the image |
| `NOWFILE_DB_PATH` | `/data/app.db` | |
| `NOWFILE_BASE_URL` | `http://localhost:8080` | set to `https://nowfile.org` |
| `NOWFILE_POSTMARK_TOKEN` | unset | secret, sign-in mail is off without it |
| `NOWFILE_MAIL_FROM` | unset | sender for sign-in mail |
| `NOWFILE_ADMIN_TOKEN` | unset | secret |

Secrets live in `/srv/clients/nowfile/env` on the box, mode 600, never in the repo:

```
NOWFILE_BASE_URL=https://nowfile.org
NOWFILE_POSTMARK_TOKEN=...
NOWFILE_MAIL_FROM=...
NOWFILE_ADMIN_TOKEN=...
```

## Run

```sh
docker pull ghcr.io/mglass/nowfile:sha-<short>
docker run -d --name nowfile --restart unless-stopped \
  --env-file /srv/clients/nowfile/env \
  -v /srv/clients/nowfile/data:/data \
  -p 79:7979 \
  -p 127.0.0.1:8091:8080 \
  ghcr.io/mglass/nowfile:sha-<short>
```

To upgrade, pull the new tag, `docker stop nowfile && docker rm nowfile`, and run the same command with the new tag. The data stays in the bind mount.

## Caddy

```
nowfile.org, www.nowfile.org {
	reverse_proxy 127.0.0.1:8091
}
```

## Admin

```sh
docker exec nowfile /usr/local/bin/nowfile admin suspend <name>
docker exec nowfile /usr/local/bin/nowfile admin release <name>
```

## Check

```sh
curl -fsS https://nowfile.org/health
finger mike@nowfile.org
docker logs nowfile    # "nowfile: starting: ..." lists the config, secrets shown only as set/unset
```
