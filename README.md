# dbsync

A lightweight, headless, Dockerized file synchronization service that keeps a
set of **explicitly configured individual files** in sync between a local
filesystem and specific Dropbox paths — without requiring the official
Dropbox desktop client.

Each file mapping is configured independently in YAML, with its own
direction (`upload`, `download`, or `bidirectional`), conflict policy, and
deletion policy. The service runs continuously inside a Docker container,
persists synchronization state in SQLite, and automatically renews Dropbox
access tokens using a long-lived OAuth 2.0 refresh token.

## Table of contents

- [Overview](#overview)
- [Architecture](#architecture)
- [Prerequisites](#prerequisites)
- [1. Create a Dropbox app](#1-create-a-dropbox-app)
- [2. Required Dropbox permissions](#2-required-dropbox-permissions)
- [3. Obtain a refresh token (OAuth setup)](#3-obtain-a-refresh-token-oauth-setup)
- [4. Environment variables](#4-environment-variables)
- [5. YAML file-mapping configuration](#5-yaml-file-mapping-configuration)
- [6. Quick start (clean machine)](#6-quick-start-clean-machine)
- [7. Dry-run mode](#7-dry-run-mode)
- [8. Running with Docker Compose](#8-running-with-docker-compose)
- [9. CLI reference](#9-cli-reference)
- [10. Conflict handling](#10-conflict-handling)
- [11. Deletion policy](#11-deletion-policy)
- [12. Restart and recovery behavior](#12-restart-and-recovery-behavior)
- [13. Logging](#13-logging)
- [14. Security considerations](#14-security-considerations)
- [15. Filesystem ownership and permissions](#15-filesystem-ownership-and-permissions)
- [16. Running the automated tests](#16-running-the-automated-tests)
- [17. Updating the Docker image](#17-updating-the-docker-image)
- [18. Troubleshooting](#18-troubleshooting)
- [19. Known limitations](#19-known-limitations)
- [20. Future improvements](#20-future-improvements)

## Overview

dbsync is **not** a general-purpose Dropbox sync client. It only synchronizes
files you explicitly list in `config.yaml`. This makes behavior predictable
and auditable, and avoids the complexity (and risk) of mirroring an entire
Dropbox account or folder tree.

Core guarantees:

- **No silent data loss.** Conflicting independent changes are detected and
  handled according to an explicit policy; the losing version is always
  preserved in a backup/conflict copy (except `skip`, which touches nothing).
- **No deletion propagation by default.** A file disappearing on one side
  never deletes its counterpart. Deletion propagation is not implemented in
  this version (see [Deletion policy](#11-deletion-policy)).
- **Crash-safe state.** Synchronization state is only ever advanced in
  SQLite *after* a transfer has completed and been verified. An interrupted
  download never corrupts the existing destination file.
- **Dry-run is truly inert.** `--dry-run` never writes to local files,
  remote files, or the state database.

## Architecture

```text
cmd/dbsync/            CLI entrypoint (auth, config, sync, status, run, version)
internal/
  config/              YAML parsing + offline validation (paths, policies, overlap/duplicate checks)
  auth/                OAuth 2.0 (authorization-code + offline access), token refresh manager
  dropbox/             Dropbox API v2 client, content-hash algorithm, retry/backoff, errors
    dropboxmock/       In-memory fake Dropbox client used by sync engine tests (no network)
  filesystem/          Path-escape-safe resolution, atomic + hash-verified file writes
  state/                SQLite-backed synchronization state (per-mapping history, cursors, conflicts)
  sync/                Core synchronization engine
    decision.go          Pure decision table: given prior/local/remote state, what action to take
    engine.go             I/O orchestration: wires config + dropbox client + state DB + filesystem
  logging/              log/slog wrapper (level/format config, secret redaction helper)
config/config.example.yaml   Example configuration
Dockerfile, compose.yaml     Container packaging
```

The synchronization engine is deliberately split into two layers:

1. **`sync.Decide`** — a pure function with no I/O. Given the last known
   synchronized state plus the current local and remote observations
   (existence, content hash, revision), it returns a `Decision` describing
   exactly what should happen (upload, download, conflict, skip, or no-op)
   and why. This is where all of the subtle correctness rules live
   (initial-sync safety, deletion non-propagation, direction semantics), and
   it is exhaustively unit-tested without touching the network or disk.
2. **`sync.Engine`** — orchestrates real I/O: reads local files, calls the
   Dropbox client, persists state, and executes the decision (including
   conflict-copy creation and dry-run short-circuiting).

This separation is what makes it possible to test synchronization logic
(including conflicts, missing files, and deletions) deterministically,
without a real Dropbox account.

## Prerequisites

- A Dropbox account and the ability to create a Dropbox API app at
  <https://www.dropbox.com/developers/apps>.
- Docker and Docker Compose v2 (`docker compose ...`), **or** a local Go
  1.23+ toolchain if you want to build/run outside Docker.
- Git (to clone/push this repository).

## 1. Create a Dropbox app

1. Go to <https://www.dropbox.com/developers/apps> and click **Create app**.
2. Choose an access type:
   - **App folder** (recommended for this use case): Dropbox creates a
     dedicated folder (`Apps/<your app name>/`) that the app can access.
     Simpler to reason about and safer — a bug or leaked token cannot touch
     anything outside that folder. Use this if all your configured `remote`
     paths can live under one folder.
   - **Full Dropbox**: the app can read/write anywhere in the account.
     Required only if your file mappings need to reference paths scattered
     across the account outside a single app folder (as in the example
     config, which uses `/Books/...` and `/References/...` at the account
     root).
3. Under **Permissions**, enable exactly these scopes (least privilege):
   - `files.metadata.read`
   - `files.content.read`
   - `files.content.write`
4. Under **Settings**, note the **App key** and **App secret** — you will
   put these into `.env` (never commit them).
5. Under **OAuth 2** → **Redirect URIs**, add `http://localhost:17643/callback`
   (or whatever port you plan to use with `dbsync auth login --port`). This
   is only needed for the local-callback login flow; it is not required if
   you plan to use the manual code-paste flow (`--manual`).

## 2. Required Dropbox permissions

dbsync requests the minimum necessary scopes: `files.metadata.read`,
`files.content.read`, and `files.content.write`. It never requests
account-wide permissions, sharing, or team-admin scopes.

## 3. Obtain a refresh token (OAuth setup)

dbsync uses OAuth 2.0 Authorization Code with `token_access_type=offline`,
which gives it a long-lived **refresh token**. The refresh token (not your
password, and not a short-lived access token) is the only credential the
running service needs; dbsync mints new 4-hour access tokens from it
automatically and never persists access tokens to disk.

Run the login flow **on your host machine** (not inside Docker — see
[Troubleshooting](#18-troubleshooting) for why):

```bash
export DROPBOX_APP_KEY=your_app_key
export DROPBOX_APP_SECRET=your_app_secret

go run ./cmd/dbsync auth login
```

This starts a short-lived HTTP listener on `127.0.0.1`, opens (or prints) an
authorization URL, and waits for Dropbox to redirect back with an
authorization code. It validates an OAuth `state` parameter to protect
against CSRF, exchanges the code for tokens, and prints:

```text
DROPBOX_REFRESH_TOKEN=sl.xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
```

Copy that line into your `.env` file. If you are on a headless/remote host
with no browser, use the manual fallback instead:

```bash
go run ./cmd/dbsync auth login --manual
```

This prints a URL to open on any machine with a browser, and prompts you to
paste the resulting authorization code back into the terminal.

Verify the refresh token works at any time with:

```bash
go run ./cmd/dbsync auth status
```

## 4. Environment variables

Copy `.env.example` to `.env` and fill in the values:

```dotenv
DROPBOX_APP_KEY=
DROPBOX_APP_SECRET=
DROPBOX_REFRESH_TOKEN=
DBSYNC_CONFIG=/app/config.yaml
```

`.env` is gitignored and must never be committed. These three Dropbox values
are the only secrets dbsync needs; no secret is ever logged (see
[Security considerations](#14-security-considerations)).

## 5. YAML file-mapping configuration

Copy `config/config.example.yaml` to `config/config.yaml` and edit it:

```yaml
sync:
  interval: 60s
  dry_run: false

  defaults:
    direction: bidirectional
    conflict_policy: keep_both
    delete_policy: ignore

  files:
    - name: Manuscript
      local: /data/books/manuscript.docx
      remote: /Books/manuscript.docx
      direction: bidirectional

    - name: Cover Artwork
      local: /data/artwork/cover.png
      remote: /Books/Artwork/cover.png
      direction: upload

    - name: Reference PDF
      local: /data/references/notes.pdf
      remote: /References/notes.pdf
      direction: download

security:
  allowed_roots:
    - /data/books
    - /data/artwork
    - /data/references

state_path: /app/state/dbsync.db

logging:
  level: info
  format: json
```

Key fields:

- `sync.interval` — how often `dbsync run` re-checks local and remote state.
- `sync.dry_run` — global dry-run switch (can also be set per-invocation
  with `sync --dry-run`).
- `sync.defaults` — applied to any mapping that doesn't override them.
- `sync.files[].direction` — `upload`, `download`, or `bidirectional`.
- `sync.files[].conflict_policy` — `keep_both` (default), `prefer_local`,
  `prefer_remote`, or `skip`.
- `sync.files[].delete_policy` — only `ignore` is currently supported
  (deletions are never propagated; see [below](#11-deletion-policy)).
- `security.allowed_roots` — every mapping's local path must resolve
  (after following symlinks) inside one of these directories. This is
  enforced even if a mapping's `local` path is itself a symlink pointing
  elsewhere, or contains `..` segments.
- `state_path` — where the SQLite state database lives; mount this on a
  persistent volume.

Validation (`dbsync config validate`) runs entirely offline and rejects:
duplicate local paths, duplicate remote paths, overlapping/ambiguous
mappings, unsupported direction/policy values, local paths outside
`allowed_roots`, symlink escapes, and any mapping that would overwrite the
config file or the state database itself.

## 6. Quick start (clean machine)

```bash
git clone <this-repo-url> db-sync-specific-files
cd db-sync-specific-files

cp .env.example .env
# edit .env: set DROPBOX_APP_KEY and DROPBOX_APP_SECRET from your Dropbox app

export DROPBOX_APP_KEY=...        # same values, for the host-side login step
export DROPBOX_APP_SECRET=...
go run ./cmd/dbsync auth login    # or: auth login --manual
# copy the printed DROPBOX_REFRESH_TOKEN=... line into .env

cp config/config.example.yaml config/config.yaml
# edit config/config.yaml: set real local/remote paths for your files

mkdir -p state
# create/point the bind-mounted host directories referenced by compose.yaml
# and by config.yaml's allowed_roots (e.g. ./books, ./artwork, ./references)

docker compose build
docker compose run --rm dbsync config validate
docker compose run --rm dbsync sync --dry-run
docker compose up -d
docker compose logs -f
```

## 7. Dry-run mode

Dry-run inspects what *would* happen without touching anything:

```bash
docker compose run --rm dbsync sync --dry-run
```

Internally, the engine computes the same decision (`sync.Decide`) it would
for a real run, but returns before any upload, download, conflict-copy
write, or state-database write occurs. Use this before every first run on a
new mapping, and any time you change `config.yaml`.

## 8. Running with Docker Compose

```bash
docker compose build     # build the image
docker compose up -d     # start the continuously running service
docker compose logs -f   # follow structured logs
docker compose down      # stop the service
```

Edit the volume list in `compose.yaml` to match your real host directories
and `config.yaml`'s `local`/`allowed_roots` paths:

```yaml
volumes:
  - ./config/config.yaml:/app/config.yaml:ro
  - ./state:/app/state
  - /host/books:/data/books
  - /host/artwork:/data/artwork
  - /host/references:/data/references
```

No network ports are published. dbsync only makes outbound HTTPS requests
to Dropbox; the one-time OAuth callback listener used by `auth login` is
run on the host, never inside the container (see
[Troubleshooting](#18-troubleshooting)).

## 9. CLI reference

```text
dbsync auth login [--manual] [--port N]   First-time Dropbox authorization; prints DROPBOX_REFRESH_TOKEN
dbsync auth status                        Checks credentials + refresh-token validity (live call)
dbsync config validate                    Validates config.yaml; no network required
dbsync sync --once                        Runs one reconciliation cycle and exits (default if no flag given)
dbsync sync --dry-run                     Same, but makes no changes to files or state
dbsync status                             Prints persisted per-mapping synchronization state
dbsync run                                Runs continuously on sync.interval until SIGTERM/SIGINT
dbsync version                            Prints the build version
```

The Docker image's default command is `run`.

## 10. Conflict handling

A conflict is detected when, compared to the last successfully synchronized
state, **both** the local and remote copies have changed. For `upload`- or
`download`-only mappings the configured direction is authoritative instead
(see [Known limitations](#19-known-limitations) for the exact semantics),
so true conflicts only arise for `bidirectional` mappings and for the
*initial* sync of any mapping where both sides already contain differing
content.

Policies (`conflict_policy`, default `keep_both`):

- **`keep_both`** — both originals are left untouched; the Dropbox version is
  downloaded alongside the local file as a uniquely named conflict copy
  (e.g. `manuscript.conflict-20240102T150405Z-ab12cd.docx`). A collision
  retry loop guarantees the conflict filename never overwrites a prior
  conflict copy.
- **`prefer_local`** — the remote file is overwritten with the local
  version, but the previous remote content is first backed up locally
  before the upload proceeds.
- **`prefer_remote`** — the local file is overwritten with the remote
  version, but the previous local content is first backed up (via the same
  atomic, hash-verified write path used for all downloads) before being
  replaced.
- **`skip`** — neither file is touched; the conflict is recorded in the
  state database and logged for manual resolution.

Every conflict is recorded in SQLite (`conflicts` table) with the mapping,
both prior hashes, and a timestamp, and is visible via `dbsync status`.

Uploads use Dropbox's conditional update (`update` mode with the previously
observed revision) so a concurrent remote edit during the upload itself is
rejected by Dropbox rather than silently clobbered; the engine treats that
as a failure, leaves the prior state baseline untouched, and retries on the
next cycle.

## 11. Deletion policy

**Deletions are never propagated**, in either direction, by design:

- A missing local file does **not** delete its Dropbox counterpart.
- A missing Dropbox file does **not** delete its local counterpart.
- On the file's *first* sync, "missing" is never interpreted as "deleted" —
  there is no prior state to compare against, so the file is simply
  propagated per the mapping's `direction`.

Only `delete_policy: ignore` is implemented and accepted by configuration
validation. Deletion propagation is explicitly **not supported** in this
version (see [Known limitations](#19-known-limitations)) rather than
partially implemented without the safeguards the task requires.

## 12. Restart and recovery behavior

All synchronization state lives in SQLite at `state_path`, mounted on a
persistent Docker volume (`./state:/app/state` by default). On restart:

- Previously synchronized hashes/revisions are loaded and compared again;
  nothing is treated as "new" or "deleted" just because the process
  restarted.
- A mapping whose previous run was interrupted mid-transfer is marked
  `in_progress` in the database and detected as such on the next run;
  because state is only ever advanced *after* a verified, completed
  transfer (see `state.CommitObserved`/`CommitSuccess`), an interrupted
  transfer never leaves a corrupted destination file or a falsely-advanced
  baseline — the mapping is simply re-evaluated and retried.
- Local downloads are always written to a temporary file, hash-verified
  against Dropbox's content hash, and only then atomically renamed into
  place — a crash mid-download leaves the previous destination file (if
  any) completely untouched.

## 13. Logging

Structured logging via `log/slog`:

```yaml
logging:
  level: info   # debug | info | warn | error
  format: json  # json | text
```

Logged events include startup, configuration validation results,
authentication failures, mapping initialization, detected local/remote
changes, uploads/downloads, conflicts, retries, cycle completion, and
graceful shutdown. Access tokens, refresh tokens, app secrets, authorization
codes, and file contents are never logged — see
[Security considerations](#14-security-considerations).

## 14. Security considerations

- Credentials are read only from environment variables (`DROPBOX_APP_KEY`,
  `DROPBOX_APP_SECRET`, `DROPBOX_REFRESH_TOKEN`); none are hardcoded,
  baked into the Docker image, or written to any log.
- The OAuth `state` parameter is a cryptographically random value validated
  on callback, providing CSRF protection for the login flow.
- Access tokens are kept in memory only and refreshed proactively before
  expiry; they are never persisted to disk.
- `security.allowed_roots` plus symlink-resolving path validation prevent a
  misconfigured or malicious `local` path from escaping the directories you
  explicitly mounted into the container.
- Configuration validation rejects mappings that would overwrite the
  config file or the state database.
- The container runs as a non-root user (distroless `nonroot`, uid 65532)
  and publishes no network ports.
- `.env`, `config/config.yaml`, and `state/` are excluded from both
  `.gitignore` and `.dockerignore` so secrets and synchronization history
  never enter version control or the image.

## 15. Filesystem ownership and permissions

The container runs as uid/gid `65532` (distroless `nonroot`). Bind-mounted
host directories (the `local` paths referenced in `config.yaml`, and the
`state/` directory) must be readable/writable by that uid on the host, e.g.:

```bash
mkdir -p state books artwork references
chown -R 65532:65532 state books artwork references   # Linux hosts
```

On macOS/Windows Docker Desktop, the Docker VM generally remaps permissions
transparently and this step is usually unnecessary; it mainly matters on
native Linux Docker hosts.

## 16. Running the automated tests

```bash
go build ./...
go vet ./...
go test ./... -v
```

Tests live alongside each package as idiomatic Go `_test.go` files (not in
a separate top-level `tests/` tree), covering: YAML parsing/validation,
Dropbox content-hash calculation, OAuth token exchange/refresh (against a
local `httptest` server, no real Dropbox account needed), path-escape and
symlink safety, atomic/verified file writes, SQLite state persistence and
restart recovery, and — most importantly — the full synchronization
decision table and engine (upload-only, download-only, bidirectional,
simultaneous changes/conflicts, initial-sync conflicts, missing files on
either side, dry-run, and interrupted-transfer safety) using an in-memory
mock Dropbox client (`internal/dropbox/dropboxmock`). No test requires
real Dropbox credentials or network access.

## 17. Updating the Docker image

```bash
git pull
go mod vendor        # if go.mod/go.sum changed
docker compose build --no-cache
docker compose up -d
```

Dependencies are vendored (`vendor/`) and committed, so image builds never
require outbound network access to the Go module proxy — only the base
images (`golang:1.23-bookworm`, `gcr.io/distroless/static-debian12:nonroot`)
need to be pulled once.

## 18. Troubleshooting

- **`auth login` works on the host but not inside the container.** The
  OAuth callback listener intentionally binds to `127.0.0.1` only, for
  security. Docker's container network namespace means that loopback
  address is not reachable through `-p` port publishing from your browser.
  Run `go run ./cmd/dbsync auth login` (or `--manual`) on the host, then
  copy the resulting `DROPBOX_REFRESH_TOKEN` into `.env` used by Compose.
- **`error: DROPBOX_APP_KEY and DROPBOX_APP_SECRET environment variables
  are required`** — `.env` is missing or not loaded; confirm Compose is
  picking it up (`docker compose config` prints resolved env values) or
  that you exported them in your shell for a bare `go run` invocation.
- **`invalid_grant` / "refresh token is invalid or has been revoked"** —
  the refresh token was revoked (e.g. user revoked app access in Dropbox
  account settings, or password/security changes invalidated it). Re-run
  `auth login` to obtain a new one.
- **Rate limited (`429`) or transient `5xx` from Dropbox** — the client
  automatically retries with exponential backoff and jitter, honoring any
  `Retry-After` header; this is expected and logged at `warn`/`debug`, not
  an error requiring action unless it persists.
- **Conflict copies piling up** — these are intentional, recoverable
  backups created by `keep_both`/`prefer_local`/`prefer_remote`; review and
  delete them manually once you've reconciled the content, or switch the
  mapping's `conflict_policy` if this is not the behavior you want.
- **`docker compose build` fails trying to reach `proxy.golang.org`** —
  dependencies should already be vendored in this repository (`vendor/`);
  verify `vendor/` exists and wasn't accidentally excluded, and that your
  Docker build context includes it (check `.dockerignore`).

## 19. Known limitations

- **Deletion propagation is not implemented.** Only `delete_policy: ignore`
  is supported; this is a deliberate, documented safety choice rather than
  a partial/unsafe implementation.
- **Remote change detection is per-mapping metadata polling**, not
  folder-level cursor/long-polling. The Dropbox client and mock already
  expose `ListFolderLatestCursor`/`ListFolderContinue`, and the state
  schema persists a `cursors` table for this purpose, but the sync engine
  does not yet use them to batch-detect remote changes across mappings
  sharing a folder — each mapping's remote metadata is fetched directly
  every cycle instead. This is correct but less efficient at scale; see
  [Future improvements](#20-future-improvements).
- **Filesystem notifications (e.g. inotify/fsnotify) are not implemented.**
  Local change detection relies solely on periodic scanning each
  `sync.interval`, which the spec explicitly allows as the reliable
  baseline for Docker bind mounts / network filesystems.
- **Concurrency is intentionally simple.** Mappings are processed
  sequentially within a cycle; this avoids any risk of two operations
  racing on the same mapping and keeps correctness guarantees easy to
  reason about, at the cost of cycle latency with very many mappings.
- Upload-session support exists in the Dropbox client for files exceeding
  the single-request upload limit, but has not been exercised against a
  real multi-gigabyte file in this environment (only unit-tested against a
  mock HTTP server) — see the manual verification note below.

### Manual verification note

Operations that require a real Dropbox account (the full OAuth login
round-trip against Dropbox's real servers, and large-file upload-session
behavior against the real API) are implemented and unit/integration tested
against mocked HTTP servers, but have not been exercised against a live
Dropbox account in this environment, since that would require real
credentials this environment does not have. Before relying on this in
production, run `auth login` against your real Dropbox app and a dry run
against a real (test) Dropbox folder as described in
[Quick start](#6-quick-start-clean-machine).

## 20. Future improvements

- Wire `ListFolderLatestCursor`/`ListFolderContinue` into the engine to
  batch-detect remote changes per shared parent folder instead of one
  `GetMetadata` call per mapping, reducing API calls for larger
  configurations.
- Optional `fsnotify`-based local change detection as a latency
  optimization layered on top of (not replacing) periodic scanning.
- An explicit, opt-in, safeguarded deletion-propagation mode (e.g.
  requiring a tombstone/trash-then-expire window) if a future use case
  needs it.
- Bounded concurrent processing of independent mappings (the spec allows
  this; it was deferred in favor of the simpler, easier-to-verify
  sequential model for this initial implementation).
- A `dbsync status --json` machine-readable output mode for external
  monitoring integrations.
