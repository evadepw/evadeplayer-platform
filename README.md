<div align="center">

# EvadePlayer Platform

### Fast, simple, and convenient backend for your video player.

Upload videos, transcode them to HLS, and get ready-to-use, signed playback URLs by ID.

[![CI](https://github.com/evadepw/evadeplayer-platform/actions/workflows/ci.yml/badge.svg)](https://github.com/evadepw/evadeplayer-platform/actions/workflows/ci.yml)
[![Go Version](https://img.shields.io/badge/Go-1.25%2B-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![Docker](https://img.shields.io/badge/Docker-Compose-2496ED?logo=docker&logoColor=white)](https://docs.docker.com/compose/)
[![OpenAPI](https://img.shields.io/badge/OpenAPI-3.1-6BA539?logo=openapiinitiative&logoColor=white)](openapi.yaml)
[![License: MIT](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)
[![Status: WIP](https://img.shields.io/badge/status-WIP-orange.svg)](#project-status)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](#contributing)

[Quick Start](#quick-start) ·
[Architecture](#architecture) ·
[API](#api-reference) ·
[Configuration](#configuration) ·
[Scaling workers](#scaling-workers) ·
[Roadmap](#roadmap)

</div>

---

> [!WARNING]
> **Project status: WIP.** EvadePlayer is under active development. The API and
> configuration are still subject to change before a tagged `v1.0` release.
> It is usable today, but pin to a commit if you depend on it.

## About

EvadePlayer is a self-hosted backend for video playback. You give it a video file;
it gives you back an HLS stream and a signed URL you can hand straight to any player.

It is built as two small Go binaries from one codebase — an **API** and a
**transcode worker** — so the heavy lifting (ffmpeg) can scale independently and
even run on separate GPU machines, while the API stays light. Media is served
directly by **nginx** from object storage, so playback traffic never touches the
application layer. PostgreSQL doubles as the transcoding queue: workers claim jobs
with `FOR UPDATE SKIP LOCKED`, heartbeat while they work, and crashed jobs are
requeued automatically — no separate queue service, no lost videos.

It handles the whole pipeline end to end:

- 📤 Video upload through a simple REST API
- 🎞️ ffmpeg transcoding to multi-quality HLS
- 🔊 Automatic multi-audio-track and subtitle (WebVTT) extraction
- 🖼️ Preview sprites for timeline scrubbing
- 🔐 Signed playback URLs with a short TTL
- 🚀 Direct media delivery via nginx
- ♻️ Crash-safe queue with retries — a dead worker never strands a video

> Frontend lives separately — bring your own player UI or wire up a dedicated frontend repo.

## Features

| | |
|---|---|
| **Simple REST API** | Upload, list, fetch, status, delete — documented with OpenAPI 3.1 + Swagger UI. |
| **HLS streaming** | Adaptive bitrate with pre-signed, expiring URLs (4 h TTL). |
| **Codecs** | H.264, H.265 and AV1 — configurable per deployment. |
| **GPU acceleration** | NVIDIA NVENC and VAAPI, with ready-made Docker Compose overlays. |
| **Multi-audio & subtitles** | Extra audio tracks become HLS audio renditions; text subtitles convert to WebVTT. |
| **Preview sprites** | Storyboard endpoint + sprite generation for hover/scrub thumbnails. |
| **Named segments** | Upload a JSON map (`intro`, `credits`, `ad`…) and fetch it back per video. |
| **Flexible auth** | Service-token auth for uploads; public or key-gated reads via a single flag. |
| **Distributed transcoding** | Add worker machines with one command each; they coordinate through PostgreSQL. |

## Architecture

```mermaid
flowchart LR
    Client["Client / Player"] -->|upload, query| Nginx
    Player["Client / Player"] -->|HLS playback| Nginx

    subgraph Edge
        Nginx["nginx<br/>(edge + media)"]
    end

    subgraph Application
        API["API<br/>(Go)"]
        Worker["Transcode workers<br/>(Go + ffmpeg, × N)"]
    end

    Nginx -->|/api| API
    API -->|insert pending row| PG[("PostgreSQL<br/>(data + queue)")]
    Worker -->|claim / heartbeat / result| PG
    Worker -->|read source / write HLS| Storage[("SeaweedFS")]
    API -->|store original| Storage
    Nginx -->|/hls, /thumbnails| Storage
    Nginx -.->|validate token| API
```

**Flow**

1. Client uploads a video with a service key → `POST /api/videos/upload`.
2. The API stores the original in SeaweedFS and inserts a `pending` row — that row *is* the queue entry.
3. A worker claims the row (`FOR UPDATE SKIP LOCKED`), runs ffmpeg — HLS variants, audio/subtitle renditions, preview sprites — and heartbeats while it works. If a worker dies, the job is requeued automatically and retried up to `TRANSCODE_MAX_ATTEMPTS`.
4. Output is written to SeaweedFS; metadata (duration, resolution, progress, sprite geometry) is written back to PostgreSQL.
5. The API returns a signed `manifest_url` once `status = ready`.
6. The player fetches the manifest and segments via nginx, which validates each token against the API before serving from storage.

### Repository layout

```
cmd/api/          API service entry point
cmd/transcoder/   transcode worker entry point
internal/         shared packages (config, handlers, services, repository, ffmpeg, worker)
docker/           Dockerfiles (api, transcoder + nvidia/vaapi variants, nginx)
nginx/            Edge config: routing, signed-media delivery, Swagger UI
migrations/       PostgreSQL schema migrations
openapi.yaml      API specification (served at /swagger/)
*.yml             Docker Compose: base + nvidia / vaapi / standalone worker
setup.sh          Configurator — interactive or scriptable (generates .env, builds, deploys)
```

## Quick Start

**Requirements:** Docker + Docker Compose. That's it — Go and ffmpeg run inside containers.

```bash
git clone https://github.com/evadepw/evadeplayer-platform.git
cd evadeplayer-platform
./setup.sh
```

`setup.sh` walks you through configuration interactively: it generates a `.env`
(auto-creating secrets for `SERVICE_KEY` and `HLS_TOKEN_SECRET`), lets you pick CPU /
NVIDIA / VAAPI acceleration, and can build and start everything for you.

Prefer no prompts? Every mode is scriptable:

```bash
./setup.sh --mode all-in-one --yes         # accept defaults, generate secrets, deploy
```

Once it's up, upload a video:

```bash
curl -X POST http://localhost/api/videos/upload \
  -H "X-Service-Key: $SERVICE_KEY" \
  -F file=@video.mp4
# → { "id": "a1b2c3d4-…", "status": "pending" }
```

Poll until it's ready, then grab the manifest:

```bash
curl http://localhost/api/videos/{id}
```

```json
{
  "id": "a1b2c3d4-...",
  "status": "ready",
  "progress": 100,
  "duration": 3723.5,
  "width": 1920,
  "height": 1080,
  "manifest_url": "http://localhost/hls-proxy/a1b2c3d4-.../master.m3u8?token=...&expires=...",
  "preview_url": "http://localhost/thumbnails/a1b2c3d4-.../preview.jpg"
}
```

Pass `manifest_url` straight to your HLS player. Interactive API docs are served at
**`http://localhost/swagger/`**.

### Common commands

```bash
make up        # build + start the full stack
make logs      # tail logs
make migrate   # run database migrations
make test      # run the Go test suite (in a container)
make lint      # run golangci-lint (in a container)
make down      # stop everything
```

## Scaling workers

Transcoding capacity is two independent knobs:

- **`TRANSCODE_WORKERS`** — concurrent transcodes per machine.
- **Number of worker machines** — as many as you want; they coordinate through
  PostgreSQL, so there is nothing else to configure.

Deploy the API server without a local worker:

```bash
./setup.sh --mode api          # or: --mode api --yes for defaults
```

Then, on each worker machine (repeat for as many as you need):

```bash
git clone https://github.com/evadepw/evadeplayer-platform.git
cd evadeplayer-platform
./setup.sh --mode worker --yes \
    MAIN_SERVER_IP=<api-server-ip> \
    POSTGRES_PASSWORD=<password from the api server .env> \
    TRANSCODE_WORKERS=4
```

Workers can be added or removed at any time. If one dies mid-job, the job is
requeued and picked up by another worker; a graceful stop (`make worker-down`)
releases in-flight jobs back to the queue without losing progress accounting.

The worker machines need network access to the API server's PostgreSQL port and
SeaweedFS filer/volume ports (setup.sh prompts for all of them).

## API Reference

Base path is `/api` when served through nginx (the default deployment). Full,
always-up-to-date spec lives in [`openapi.yaml`](openapi.yaml) and renders at
`/swagger/`.

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `POST` | `/videos/upload` | Service key | Upload a video (optional `segments` JSON field) |
| `GET`  | `/videos` | Public / key | List videos (`page`, `page_size`) |
| `GET`  | `/videos/{id}` | Public / key | Video details + `manifest_url` |
| `GET`  | `/videos/{id}/status` | Public / key | Transcode status + progress |
| `GET`  | `/videos/{id}/storyboard` | Public / key | Sprite cues for scrubbing |
| `GET`  | `/videos/{id}/segments` | Public / key | Named time intervals for the video |
| `POST` | `/videos/tokens` | Public / key | Batch-issue playback tokens by video id |
| `GET`  | `/videos/{id}/download` | Service key | Download the original file |
| `DELETE` | `/videos/{id}` | Service key | Delete a video and all its media |
| `GET`  | `/healthz` | — | Health check |

A video's `status` moves through `pending → processing → ready` (or `failed`
after `TRANSCODE_MAX_ATTEMPTS` failed attempts, with the error preserved in
`error_message`). The `manifest_url` is only present once `status = ready`.

## Authentication

Uploads, deletes and original downloads **always** require the `X-Service-Key` header.

Read access is governed by a single flag:

```dotenv
READ_PUBLIC=true     # anyone can fetch video info and manifests (default)
# READ_PUBLIC=false  # X-Service-Key required for reads too

SERVICE_KEY=change-me
HLS_TOKEN_SECRET=change-me
```

Regardless of `READ_PUBLIC`, **HLS manifests and segments are always signed** —
tokens expire after 4 hours, and nginx validates every segment request against the
API before serving bytes. (Disable with `HLS_REQUIRE_TOKEN=false` if you serve
fully public content.)

## Configuration

The most common variables — run `./setup.sh` to set these interactively, or see
[`.env.example`](.env.example) for the complete annotated list.

| Variable | Description |
|----------|-------------|
| `SERVICE_KEY` | Key required for upload (and reads when `READ_PUBLIC=false`) |
| `HLS_TOKEN_SECRET` | Secret used to sign HLS URLs |
| `READ_PUBLIC` | `true` = open reads, `false` = key required |
| `PUBLIC_HOST` | Public base URL, e.g. `https://cdn.example.com` |
| `NGINX_PORT` | Host port exposed by nginx |
| `MAX_UPLOAD_SIZE_GB` | Max upload size in GB (default: `50`) |
| `CORS_ORIGINS` | Allowed CORS origins |
| `LOG_LEVEL` | `debug`, `info`, `warn`, `error` (default: `info`) |
| `TRANSCODE_ACCEL` | `cpu`, `nvidia`, or `vaapi` |
| `TRANSCODE_CODECS` | e.g. `h264,h265,av1` |
| `TRANSCODE_QUALITIES` | e.g. `360p,720p,1080p` |
| `TRANSCODE_WORKERS` | Concurrent transcode jobs per machine |
| `TRANSCODE_MAX_ATTEMPTS` | Attempts before a video is marked `failed` (default: `3`) |

Storage and database connection settings (`SEAWEEDFS_*`, `POSTGRES_*`) are wired
up by Compose and `setup.sh` and rarely need manual edits for a single-host
deployment.

## GPU acceleration

```bash
make up                # CPU (default)
make worker-up-nvidia  # NVIDIA NVENC (standalone worker)
make worker-up-vaapi   # Intel/AMD VAAPI (standalone worker)
```

Compose overlays (`docker-compose.nvidia.yml`, `docker-compose.vaapi.yml`) and
matching Dockerfiles ship in the repo. `setup.sh` auto-detects available
acceleration and sets `TRANSCODE_ACCEL` accordingly.

## Project status

EvadePlayer is **work in progress**. The core pipeline is functional and covered by
Go tests, lint, and image builds run in CI on every push, but interfaces may change
before a stable release.

### Roadmap

- [x] Upload → HLS transcoding pipeline
- [x] H.264 / H.265 / AV1, with NVENC & VAAPI
- [x] Multi-audio tracks and WebVTT subtitles
- [x] Signed playback URLs + nginx token validation
- [x] OpenAPI 3.1 spec + Swagger UI
- [x] Crash-safe PostgreSQL job queue with retries
- [x] One-command worker deployment, any number of machines
- [ ] Tagged `v1.0` with a frozen API
- [ ] Webhooks / callbacks on transcode completion
- [ ] Metrics & observability endpoints
- [ ] Helm chart for Kubernetes deployments

## Contributing

Contributions are welcome. Everything lives in one Go module:

```bash
make test    # tests (containerized)
make lint    # golangci-lint (containerized)
# or natively: go test ./... && go vet ./...
```

Open an issue to discuss larger changes before sending a PR, and please keep new
endpoints documented in `openapi.yaml`.

## License

[MIT](LICENSE)
