# Everything Go MCP (egmcp)

> Management plane for the [Model Context Protocol](https://modelcontextprotocol.io). Point it at any middleware — MySQL, PostgreSQL, S3/MinIO, Aliyun OSS, local filesystems, OpenAPI / Swagger APIs, generic HTTP — and a few clicks later you have a standards-compliant MCP server your LLM clients (Claude Desktop, Cursor, Cline, …) can talk to.

[中文文档 (README.zh-CN.md)](README.zh-CN.md)

## Status

✅ **M0–M6 shipped.** The platform now ships with seven built-in
connectors (filesystem, MySQL, PostgreSQL, S3 / MinIO, Aliyun OSS,
Swagger / OpenAPI, generic HTTP `fetch`), a third-party Go Plugin
mechanism, the full MCP 2025-03-26 protocol surface, and an
admin web console with schema-driven instance creation.

| ID | Title | Status |
| --- | --- | --- |
| M0 | Engineering skeleton | ✅ |
| M1 | Config layer + auth + UI scaffold | ✅ |
| M2 | MCP protocol + filesystem connector | ✅ |
| M3 | MySQL / PostgreSQL | ✅ |
| M4 | OSS / S3 (MinIO) | ✅ |
| M5 | Swagger / OpenAPI | ✅ |
| M6 | Go Plugin loader | ✅ |
| M7 | Git + fetch extras | ✅ |
| M8 | Audit, metrics, GA | ✅ |

The design and the milestone plan live in
[`docs/superpowers/specs/`](docs/superpowers/specs/).

---

## Quick start (Docker)

```bash
docker build -t egmcp:dev .
docker run --rm -p 8080:8080 -v egmcp-data:/data --name egmcp-dev egmcp:dev

# in another shell
curl -s http://localhost:8080/healthz
# {"instance_id":"/data","status":"ok","uptime":"1s"}
```

On first boot the platform:

1. Creates `/data/configs/admin.yaml` with safe defaults.
2. Generates a random admin password and a JWT signing secret.
3. Prints the password to the container log exactly once:

```bash
docker logs egmcp-dev | grep first-boot
```

Open the dashboard at `http://localhost:8080/`.

## Quick start (local development)

Prerequisites: Go 1.22+, Node 18+, (optional) Docker for the dev
fixtures in `deploy/docker/docker-compose.dev.yml`.

```bash
make web-install         # install frontend deps
make web-build           # build web/dist (mandatory before `make build`)
make build               # compile backend into bin/egmcp
make run                 # run from ./configs/admin.yaml
# or:
make docker-build        # build the image even without local Go
```

---

## Built-in connectors

| Connector | Tools | Use case |
| --- | --- | --- |
| `filesystem` | `read_file`, `write_file`, `list_dir`, `delete_file`, `search`, `fs://tree` resource | Sandbox a directory and expose it as MCP |
| `mysql` | `sql_query`, `list_databases`, `list_tables`, `describe_table` | Read-mostly SQL over MySQL/MariaDB |
| `postgres` | `sql_query`, `list_schemas`, `list_tables`, `describe_table` | Read-mostly SQL over PostgreSQL |
| `s3` | `put_object`, `get_object`, `delete_object`, `list_objects`, `presign_get`, `list_buckets` | S3-compatible storage (AWS, MinIO, R2, …) |
| `oss` | same as `s3` | Aliyun OSS |
| `swagger` | one tool per OpenAPI operation | Wrap any OpenAPI 3.0/3.1 document |
| `fetch` | `get`, `post`, `put`, `delete`, `head` | Catch-all HTTP client |
| third-party | (your code) | `go build -buildmode=plugin` against `pkg/connector` |

Three security policies are enforced uniformly:

- **Connector-level** auth flows (`bearer`, `apiKey`, `basic`,
  `none`).
- **Per-instance** API keys (`POST /instances/{slug}/rotate-key`)
  issued in addition to the admin JWT.
- **MCP transport** admission: `Authorization: Bearer <jwt>` or
  `X-Instance-Key: <key>` (or `?key=` query parameter).

---

## MCP transport

The platform implements the [Model Context Protocol
2025-03-26](https://modelcontextprotocol.io) on top of
[`modelcontextprotocol/go-sdk`](https://github.com/modelcontextprotocol/go-sdk).

| Method & Path | Transport | Auth |
| --- | --- | --- |
| `ANY  /mcp/{slug}` | Streamable HTTP (modern) | bearer or instance key |
| `GET /mcp/{slug}/sse` + `POST /mcp/{slug}/messages` | legacy SSE | bearer or instance key |
| `GET /mcp/{slug}/openapi.json` | OpenAPI 3.1 export | public |

Configure the client (e.g. Claude Desktop) with:

```json
{
  "mcpServers": {
    "my-instance": {
      "type": "http",
      "url": "http://localhost:8080/mcp/my-slug",
      "headers": {
        "Authorization": "Bearer <jwt-or-instance-key>"
      }
    }
  }
}
```

---

## Admin REST API (all under `/api/v1`)

| Method & Path | Purpose |
| --- | --- |
| `POST /auth/login` | public; returns JWT |
| `POST /auth/refresh` | re-issue token |
| `GET /me` | admin profile |
| `GET /connectors/builtin` | list registered connector types + ConfigSchema |
| `GET /instances` | list all MCP instances |
| `POST /instances` | create (validates slug, dedupes by slug) |
| `GET /PUT /DELETE /instances/{slug}` | read / replace / delete |
| `POST /instances/{slug}/test` | HealthCheck each connector |
| `POST /instances/{slug}/rotate-key` | issue a new API key |
| `GET /plugins` | list scanned + loaded plugins |
| `POST /plugins/upload` | multipart upload (`file=…`); max 50 MiB |
| `DELETE /plugins/{name}` | unload + delete file |
| `GET /audit?n=N` | recent audit events (default 50, capped 500) |
| `GET /metrics` | Prometheus exposition |
| `GET /audit/tail` | last 50 lines of today's audit log as JSONL |
| `GET /healthz` / `/readyz` | liveness |

---

## Project layout

```text
cmd/egmcp/                     # entrypoint
internal/
  config/                      # YAML loading, ENV substitution, first-boot bootstrap
  log/                         # zap wrapper (JSON to stdout)
  core/                        # router, instance registry
  server/                      # HTTP mux, middleware, static fallback
  auth/                        # JWT, bcrypt
  store/                       # YAML instance store + fsnotify + file lock
  api/                         # /api/v1/* handlers (Gin)
  mcp/                         # MCP protocol adapter (Streamable HTTP + SSE)
  audit/                       # request audit log (M8)
  plugin/                      # third-party Go plugin loader (.so/.dll)
  connectors/builtin/
    filesystem/                 # sandboxed directory MCP
    mysql/                      # MySQL via go-sql-driver/mysql
    postgres/                   # PostgreSQL via pgx/v5
    s3/                         # S3 / MinIO via aws-sdk-go-v2
    oss/                        # Aliyun OSS
    swagger/                    # OpenAPI 3.x via kin-openapi
    fetch/                      # generic HTTP GET/POST/PUT/DELETE
pkg/
  connector/                   # Connector SDK (stable API for plugin authors)
  sqlconn/                     # shared SQL policy (read-only, timeout, allow-list)
  objectstore/                 # shared object-storage Backend interface
web/                           # Vite + React + TS + AntD admin console
examples/plugin-hello/         # minimal third-party plugin template
configs/                       # admin.yaml + instance examples
deploy/docker/                 # Dockerfile, entrypoint.sh, docker-compose.dev.yml
docs/superpowers/specs/        # design + plan docs
```

---

## Milestone deep-dives

### M1 — admin console + REST CRUD

Open `http://localhost:8080/`:

1. Sign in with the admin credentials printed on first boot.
2. Create an instance (`New instance`):
   - **Basics**: pick a slug (`/mcp/{slug}` URL).
   - **Type**: pick a connector type. The list is auto-generated
     from `Manifest.ConfigSchema`.
   - **Config**: schema-driven form with `password` fields hidden
     automatically.
3. Manage in the list view: refresh, **Test** (HealthCheck every
   connector), delete. Each detail page shows the rendered config
   and a "Copy JSON" button producing an `mcpServers` snippet for
   Claude Desktop / Cursor.

### M2 — filesystem connector

| Tool | Description |
| --- | --- |
| `filesystem__read_file` | Read a UTF-8 text file |
| `filesystem__write_file` | Write a UTF-8 text file (disabled read-only) |
| `filesystem__list_dir` | List directory children (with optional hidden) |
| `filesystem__delete_file` | Delete a file or empty directory (destructive) |
| `filesystem__search` | Recursive name search (case-insensitive) |

Plus a single resource `fs://tree` returning the directory tree as JSON.

**Sandbox**: every incoming path is canonicalised with
`filepath.Clean("/" + p)` and joined to the configured root; symlinks
are resolved and rejected if they point outside the root.

Verified end-to-end:

| Step | Result |
| --- | --- |
| `POST /mcp/sandbox` initialize | 200 + `Mcp-Session-Id` |
| `notifications/initialized` | 202 |
| `tools/list` | 5 tools with full JSON schemas |
| `tools/call filesystem__read_file` | returns file content |
| `tools/call filesystem__read_file ../etc/passwd` | `isError: true` |
| `GET /mcp/sandbox/openapi.json` | valid OpenAPI 3.1 |

### M3 — MySQL + PostgreSQL

Shared `pkg/sqlconn` enforces three policies uniformly:

- **Read-only** (default `true`) rejects non-`SELECT`/`WITH`/`SHOW`/
  `EXPLAIN`/`DESCRIBE`/`TABLE`/`VALUES` statements via a leading-keyword
  classifier (defence-in-depth, not a parser).
- **Statement timeout** (default 30s) bounds any single query.
- **Table allow-list**: when non-empty, queries referencing tables
  outside the set fail closed (regex-based reference extraction).

Tool surface:

| Tool | Description |
| --- | --- |
| `sql_query` | Run a statement, return rows as JSON |
| `list_databases` / `list_schemas` | List visible databases/schemas |
| `list_tables` | List tables in the configured database/schema |
| `describe_table` | Column metadata |

Local fixtures via `deploy/docker/docker-compose.dev.yml`:

```bash
docker compose -f deploy/docker/docker-compose.dev.yml up -d
```

### M4 — S3 + Aliyun OSS

Shared `pkg/objectstore` exposes a `Backend` interface:
`Stat`, `Get`, `Put`, `Delete`, `List`, `PresignGet`, `ListBuckets`.

| Tool | Description |
| --- | --- |
| `put_object` | Upload (text or base64) |
| `get_object` | Download as base64 |
| `delete_object` | Idempotent delete |
| `list_objects` | List under an optional prefix |
| `presign_get` | Time-limited GET URL |
| `list_buckets` | List buckets visible to the credentials |

The docker-compose fixture includes a MinIO container on
`localhost:9000` (root user `minio` / `minio12345`).

### M5 — Swagger / OpenAPI

Given a spec URL (or file), generates one MCP tool per path+method
and forwards invocations to the upstream service.

Tool names: `call__<tag>__<method>__<path>`.
Parameters become a flat object keyed by parameter name; path tokens
are substituted at invocation; request body is sent under a `body` key.

Authentication (configured via the connector):

| Type | Config |
| --- | --- |
| `none` | (no auth headers added) |
| `bearer` | `auth.bearer` → `Authorization: Bearer <token>` |
| `apiKey` | `auth.api_key_name / api_key_value` in `header` (default) or `query` |
| `basic` | `auth.basic_user / basic_pass` via `SetBasicAuth` |

An in-process token-bucket rate limiter (default 600 RPM) protects
the upstream; `timeout_seconds` bounds each call.

### M6 — Go Plugin loader

Third parties can extend the platform by compiling a Go file into a
shared library with `go build -buildmode=plugin`. Drop the result
into `data/plugins/` (or upload via `POST /api/v1/plugins/upload`) and
it becomes a first-class connector.

The plugin must export a top-level variable named `Connector` (or
`NewConnector` as a fallback) of type
`connector.Connector` from `pkg/connector`:

```go
//go:build ignore

package main

import (
    "context"
    "encoding/json"
    "github.com/processcrash/egmcp/pkg/connector"
)

var Connector connector.Connector = &hello{}

type hello struct{ prefix string }

func (h *hello) Manifest() connector.Manifest {
    return connector.Manifest{
        Name: "hello", Version: "0.1.0",
        DisplayName: "Hello (sample)",
        Capabilities: []string{connector.CapabilityTools},
        ConfigSchema: connector.JSONSchema(`{ "type":"object","properties":{ "prefix": {"type":"string"} } }`),
    }
}
// ... Init, HealthCheck, Shutdown, Tools, InvokeTool
```

Build:

```bash
# Linux / macOS
go build -buildmode=plugin -o hello.so ./examples/plugin-hello

# Windows (PowerShell)
go build -buildmode=plugin -o hello.dll ./examples/plugin-hello
```

> **Limitation**: the plugin must be compiled with the same Go
> toolchain and C library family as the platform binary (Linux
> glibc / Windows MSVC).

---

## Configuration

`configs/admin.yaml` (auto-created on first boot):

```yaml
server:
  listen: ":8080"

auth:
  admin_username: admin
  admin_password_hash: "$2a$..."   # bcrypt; first-boot value auto-generated
  jwt_secret: "..."                 # 64-hex; first-boot value auto-generated
  jwt_ttl: 12h

data_dir: ./data
instances_dir: ./data/instances
plugins_dir: ./data/plugins
log_level: info
```

Secret references (`${VAR}` and `${VAR:-default}`) are resolved at
startup. The default path resolution is shell-style: `${VAR}` requires
the env var to be set (and non-empty to substitute); `${VAR:-default}`
falls back to `default` when the var is unset or empty.

### M7 — fetch + git

The `fetch` connector is a thin HTTP client: `get`, `post`, `put`,
`delete`, `head`. Configure a `base_url` and the four standard auth
flows; the connector prepends the base URL to every per-call path and
forwards the request. Bodies are UTF-8 by default; pass `base64: true`
to upload binary. Response bodies are capped (default 8 MiB) with a
`truncated` flag when the cap kicks in.

The `git` connector shells out to the `git` CLI over a local
repository path. It exposes read-only operations:

| Tool | Description |
| --- | --- |
| `log` | Recent commits (configurable count + branch) |
| `show` | A specific commit's metadata + diffstat |
| `diff` | Between two refs (default: working tree vs HEAD) |
| `status` | Porcelain v1 status |
| `branches` | Local + remote branches, with the current one marked |
| `blame` | Per-line authorship for a file (optionally a line range) |
| `search` | `git grep` over tracked files |

Both connectors are first-class in the admin console: select them
in the create-wizard, fill in the schema-driven form, and the
resulting tool inventory is exposed under `/mcp/{slug}`.

### M8 — audit, metrics, GA

The platform exposes two operational surfaces that the
[design doc](docs/superpowers/specs/2026-07-02-egmcp-design.md#13-security--observability)
calls out:

- **`GET /metrics`** — Prometheus exposition, served from a private
  registry to avoid noise from imported libraries. Series:

  | Name | Type | Labels |
  | --- | --- | --- |
  | `egmcp_mcp_calls_total` | counter | `instance`, `connector`, `tool`, `status` |
  | `egmcp_mcp_call_duration_seconds` | histogram | `instance`, `connector`, `tool` |
  | `egmcp_http_requests_total` | counter | `path`, `method`, `status` (1xx–5xx) |
  | `egmcp_active_instances` | gauge | — |

- **Audit log** — every MCP call (and admin write) is appended to
  `data/audit/YYYY-MM-DD.jsonl`. The admin console and `GET
  /audit/tail` (text/x-ndjson) and `GET /api/v1/audit?n=50` (JSON)
  endpoints both surface the same stream; older days' files are
  retained on disk for archival.

With M8 the project hits the milestones the design document
promised: every connector in the matrix is shipped, the MCP
transport is at the 2025-03-26 spec, and the platform has
production-grade observability. The GA cut is the same `docker run`
that has worked since M0 — no new operational requirements.

## License

T
TBD
