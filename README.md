# Everything Go MCP (egmcp)

> Management plane for the [Model Context Protocol](https://modelcontextprotocol.io). Point it at any middleware — MySQL, PostgreSQL, S3/MinIO, Aliyun OSS, local filesystems, Swagger APIs — and a few clicks later you have a standards-compliant MCP server your LLM clients can talk to.

**Status:** M0 (engineering skeleton). Backend boots, `/healthz` returns 200, single-image build works. See [docs/superpowers/specs/2026-07-02-egmcp-design.md](docs/superpowers/specs/2026-07-02-egmcp-design.md) for the design and [2026-07-02-egmcp-plan.md](docs/superpowers/specs/2026-07-02-egmcp-plan.md) for the milestone plan.

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
3. Prints the password to the container log exactly once.

Look it up with:

```bash
docker logs egmcp-dev | grep first-boot
```

Open the dashboard at `http://localhost:8080/` (M1 will add login form; M0 returns a static placeholder).

## Quick start (local development)

Prerequisites: Go 1.22+, Node 18+.

```bash
make web-install         # install frontend deps
make web-build           # build web/dist (mandatory before `make build`)
make build               # compile backend into bin/egmcp
make run                 # run from ./configs/admin.yaml
# or:
make docker-build        # build the image even without local Go
```

## Project layout

```text
cmd/egmcp/                  # entrypoint
internal/
  config/                   # YAML loading, ENV substitution, first-boot bootstrap
  log/                      # zap wrapper (JSON to stdout)
  core/                     # router, instance registry (M0: stub)
  server/                   # HTTP mux, middleware, static fallback
  auth/                     # (M1) JWT, bcrypt
  store/                    # (M1) YAML instance store + fsnotify
  connectors/builtin/       # (M2+) filesystem, mysql, postgres, oss, s3, swagger
  audit/                    # (M8) request audit log
pkg/connector/              # Connector SDK (stable API for plugin authors)
web/                        # Vite + React + TS + AntD admin console
plugins/                    # third-party .so/.dll connectors
configs/                    # admin.yaml + instance examples
deploy/docker/              # Dockerfile + entrypoint
docs/superpowers/specs/     # design + plan docs
```

## Milestones

| ID | Title | Status |
| --- | --- | --- |
| M0 | Engineering skeleton | ✅ |
| M1 | Config layer + auth + UI scaffold | ✅ |
| M2 | MCP protocol + filesystem connector | ✅ |
| M3 | MySQL / PostgreSQL | ✅ |
| M4 | OSS / S3 (MinIO) | ✅ |
| M5 | Swagger / OpenAPI | ✅ |
| M6 | Go Plugin loader | ✅ |
| M7 | Research-driven extras (Git, Fetch, …) | ⏳ |
| M8 | Audit, metrics, docs, GA | ⏳ |

## M2 walkthrough

The platform now speaks the [Model Context Protocol](https://modelcontextprotocol.io).

After creating an instance of the **filesystem** connector type, point any
MCP-compatible client (Claude Desktop, Cursor, Cline, …) at:

```text
http://<host>:8080/mcp/<slug>
```

…with `Authorization: Bearer <admin JWT>` (or a per-instance API key via
`X-Instance-Key: <key>` / `?key=<key>`).

The MCP layer is implemented in `internal/mcp` on top of
[`modelcontextprotocol/go-sdk`](https://github.com/modelcontextprotocol/go-sdk):

- **Streamable HTTP** at `/mcp/{slug}` (the 2025-03-26 transport).
- **Legacy SSE** at `/mcp/{slug}/sse` + `/mcp/{slug}/messages`.
- **OpenAPI 3.1** export at `/mcp/{slug}/openapi.json`.

The filesystem connector exposes:

| Tool | Description |
| --- | --- |
| `filesystem__read_file` | Read a UTF-8 text file |
| `filesystem__write_file` | Write a UTF-8 text file (disabled read-only) |
| `filesystem__list_dir` | List directory children (with optional hidden files) |
| `filesystem__delete_file` | Delete a file or empty directory (destructive) |
| `filesystem__search` | Recursive name search (case-insensitive) |

Plus a single resource `fs://tree` returning the directory tree as JSON.

**Sandbox**: every incoming path is canonicalised with
`filepath.Clean("/" + p)` and joined to the configured root; symlinks are
resolved and rejected if they point outside the root. Path traversal,
absolute paths, and symlink escapes all fail closed.

Verified end-to-end (with the embedded binary):

| Step | Result |
| --- | --- |
| `POST /mcp/sandbox` initialize | 200 + `Mcp-Session-Id` |
| `notifications/initialized` | 202 |
| `tools/list` | 5 tools with full JSON schemas |
| `tools/call filesystem__read_file` | returns file content |
| `tools/call filesystem__read_file ../etc/passwd` | `isError: true` |
| `GET /mcp/sandbox/openapi.json` | valid OpenAPI 3.1 |

## M3 walkthrough

Two new SQL connectors, **mysql** and **postgres**, share a
`pkg/sqlconn` package that enforces the platform's security policies
uniformly across drivers:

- **Read-only mode** (default `true`) rejects any statement whose
  first significant keyword is not `SELECT`/`WITH`/`SHOW`/`EXPLAIN`/
  `DESCRIBE`/`TABLE`/`VALUES`.
- **Statement timeout** (default 30s) bounds any single query.
- **Row cap** truncates oversized result sets and flags `truncated`.
- **Table allow-list**: when non-empty, queries referencing tables
  outside the set fail closed. The check uses a small regex-based
  parser (good enough for typical queries; not a full SQL grammar).

Tool surface (same shape for both drivers):

| Tool | Description |
| --- | --- |
| `sql_query` | Run a statement, return rows as JSON |
| `list_databases` / `list_schemas` | List visible databases/schemas |
| `list_tables` | List tables in the configured database/schema |
| `describe_table` | Column metadata for a table |

Bring up local fixtures for end-to-end testing:

```bash
docker compose -f deploy/docker/docker-compose.dev.yml up -d
```

Then point a mysql connector at `user:pass@tcp(localhost:3306)/egmcp_test`
and a postgres connector at `postgres://egmcp:egmcp@localhost:5432/egmcp_test?sslmode=disable`.

## M4 walkthrough

Two object-storage drivers, **s3** (MinIO-compatible via
[`aws-sdk-go-v2`](https://github.com/aws/aws-sdk-go-v2)) and **oss**
(Aliyun OSS via the official SDK), share a `pkg/objectstore` package
that exposes one `Backend` interface to MCP: `Stat`, `Get`, `Put`,
`Delete`, `List`, `PresignGet`, `ListBuckets`.

Tool surface (same shape for both drivers):

| Tool | Description |
| --- | --- |
| `put_object` | Upload (text or base64) |
| `get_object` | Download as base64 |
| `delete_object` | Idempotent delete |
| `list_objects` | List under an optional prefix |
| `presign_get` | Time-limited GET URL |
| `list_buckets` | List buckets visible to the credentials |

Both connectors register in the admin console after restart; selecting
them in the create-wizard yields a schema-driven form (access key,
secret key, region/endpoint, optional default bucket).

A **MinIO** fixture is included in `deploy/docker/docker-compose.dev.yml`
with healthcheck on `localhost:9000` (root user `minio` /
`minio12345`).

## M5 walkthrough

The **swagger** connector wraps any OpenAPI 3.0 or 3.1 document. Given
a spec URL (or file), it generates one MCP tool per path+method and
forwards invocations to the upstream service.

Tool names follow `call__<tag>__<method>__<path>`, e.g. a spec with
`/pets/{id} GET` becomes `call__pets__GET__pets_id`. Parameters
become a flat object keyed by parameter name; path tokens are
substituted from the supplied args at invocation time; the request
body is sent under a `body` key.

Authentication supports the three most common OpenAPI flows:

| Type | Config |
| --- | --- |
| `none` | (no auth headers added) |
| `bearer` | `auth.bearer` is sent as `Authorization: Bearer <token>` |
| `apiKey` | `auth.api_key_name / api_key_value` in `header` (default) or `query` |
| `basic` | `auth.basic_user / basic_pass` via `SetBasicAuth` |

An in-process token-bucket rate limiter (default 600 RPM, configurable
through `max_rpm`) protects the upstream from runaway agents. Set
`timeout_seconds` to bound each upstream call.

## M6 walkthrough — Go Plugins

Third parties can extend the platform by compiling a Go file into a
shared library with `go build -buildmode=plugin`. Drop the result into
`data/plugins/` (or upload via `POST /api/v1/plugins/upload`) and it
becomes a first-class connector.

The plugin must export a top-level variable named `Connector` (or
`NewConnector` as a fallback) of type
`connector.Connector` from `github.com/processcrash/egmcp/pkg/connector`:

```go
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
        ConfigSchema: connector.JSONSchema(`{ "type":"object", "properties": { "prefix": {"type":"string"} } }`),
    }
}
// ... Init, HealthCheck, Shutdown, Tools, InvokeTool
```

Build it once and ship:

```bash
# Linux / macOS
go build -buildmode=plugin -o hello.so .

# Windows (PowerShell)
go build -buildmode=plugin -o hello.dll .
```

REST surface (admin only):

| Method & Path | Purpose |
| --- | --- |
| `GET /api/v1/plugins` | list scanned plugins + load status |
| `POST /api/v1/plugins/upload` | multipart upload (`file=…`); max 50 MiB |
| `DELETE /api/v1/plugins/{name}` | unload + delete file |

Limitations: the plugin must be compiled with the same Go toolchain
and C library family as the platform binary (Linux glibc / Windows
MSVC). See `examples/plugin-hello/` for a working template.

## M1 walkthrough

After `docker run`, open `http://localhost:8080/`:

1. **Sign in** with the admin credentials printed to container logs.
2. **Create an instance** (`New instance`):
   - Step 1: pick a slug (becomes `/mcp/{slug}`).
   - Step 2: pick a connector type (built-ins register in M2+).
   - Step 3: the schema-driven form is auto-generated from the connector's `Manifest.ConfigSchema`.
3. **Manage** instances in the list view: refresh, test, delete. Each instance page shows the connector config and a "Copy JSON" button that produces an `mcpServers` snippet ready to paste into Claude Desktop / Cursor.

The REST surface (all under `/api/v1`):

| Method & Path | Purpose |
| --- | --- |
| `POST /auth/login` | public; returns JWT |
| `GET /me` | admin profile |
| `GET /connectors/builtin` | list registered connector types |
| `GET/POST /instances` | list / create |
| `GET/PUT/DELETE /instances/{slug}` | read / replace / delete |
| `POST /instances/{slug}/test` | HealthCheck each connector |
| `POST /instances/{slug}/rotate-key` | issue a new API key |
| `GET /plugins` | plugin list (empty until M6) |
| `GET /healthz` / `/readyz` | liveness |

## Configuration

`admin.yaml` (auto-created on first boot):

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

Secret references (`${VAR}` and `${VAR:-default}`) are resolved at startup.

## License

TBD
