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

```
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
|----|-------|--------|
| M0 | Engineering skeleton (this release) | ✅ |
| M1 | Config layer + auth + UI scaffold | ⏳ |
| M2 | MCP protocol + filesystem connector | ⏳ |
| M3 | MySQL / PostgreSQL | ⏳ |
| M4 | OSS / S3 (MinIO) | ⏳ |
| M5 | Swagger / OpenAPI | ⏳ |
| M6 | Go Plugin loader | ⏳ |
| M7 | Research-driven extras (Git, Fetch, …) | ⏳ |
| M8 | Audit, metrics, docs, GA | ⏳ |

See [the plan](docs/superpowers/specs/2026-07-02-egmcp-plan.md) for per-milestone tasks and acceptance criteria.

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
