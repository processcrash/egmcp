# Everything Go MCP (egmcp) — 设计稿

> 日期：2026-07-02
> 状态：草案 → 设计确认后进入实现阶段

## 1. 目标与定位

**Everything Go MCP (egmcp)** 是一个 MCP（Model Context Protocol）管理平台，通过可视化界面将任意中间件（数据库、对象存储、文件系统、HTTP API 等）一键转换为符合标准的 MCP Server，让 LLM 客户端（Claude Desktop、Cursor、Cline 等）即开即用。

核心承诺：

- **零编码接入**：UI 配置 → 标准 MCP 端点。
- **多实例隔离**：同一部署上可创建任意多个独立 MCP Server，每个实例拥有自己的工具集合与访问凭证。
- **可扩展**：内置常用连接器，同时允许第三方以 Go Plugin 形式扩展。
- **开箱即用**：单镜像、`docker run` 即可启动。

## 2. 用户场景

1. **个人/小团队**：本地启动一个 Docker 容器，UI 中点几下创建"MySQL MCP 实例"，复制 URL 粘贴到 Claude Desktop，即可让 LLM 查询数据库。
2. **多团队共享服务**：平台管理员在控制台为不同团队创建独立 MCP 实例，授予 API Key，互不干扰。
3. **扩展连接器**：合作方按 SDK 编写自己的数据库/服务连接器，编译为 `.so`/`.dll` 放入 `plugins/` 目录，平台重启加载。

## 3. 核心架构

```
┌───────────────────────────────────────────────────────────┐
│            egmcp Platform (single Go binary)              │
│                                                           │
│  ┌────────────────┐    ┌──────────────────────────────┐   │
│  │  Admin Console │    │  MCP HTTP+SSE Endpoints      │   │
│  │  /api/v1/*     │    │  /mcp/{slug} (Streamable)    │   │
│  │  /             │    │  /mcp/{slug}/sse + messages  │   │
│  │  (JWT)         │    │  (per-configurable instance) │   │
│  └────────────────┘    └──────────────────────────────┘   │
│           │                       │                        │
│           ▼                       ▼                        │
│  ┌──────────────────────────────────────────────────────┐  │
│  │         Core: ConfigStore + Router                   │  │
│  │   configs/instances/*.yaml   plugins/*.so            │  │
│  └──────────────────────────────────────────────────────┘  │
│           │                                                │
│           ▼                                                │
│  ┌──────────────────────────────────────────────────────┐  │
│  │     Connector Registry (in-process + plugins)        │  │
│  │   Filesystem | MySQL | PostgreSQL | OSS | MinIO |    │  │
│  │   Swagger | ...                                      │  │
│  └──────────────────────────────────────────────────────┘  │
│           │                                                │
│           ▼                                                │
│  Targets: 上游中间件（数据库 / 对象存储 / HTTP API / ...）   │
└───────────────────────────────────────────────────────────┘
```

**关键定位**：平台本身不是单一的 MCP Server。每个配置项 `slug` 对应一个独立的 MCP 端点（`/mcp/{slug}`）。平台在协议层做分发与隔离，承载所有实例。

## 4. 关键概念

| 概念 | 含义 |
|---|---|
| **Instance（MCP 实例）** | 平台"创建"的最小单元。一个 slug，一个端点 `/mcp/{slug}`，包含若干已启用的 Connector |
| **Connector（连接器）** | 中间件适配模块，实现 SDK 接口，向 MCP 协议暴露 Tools/Resources/Prompts |
| **Plugin（插件）** | 第三方开发者提供的 Connector，以 `.so`/`.dll` 形式在启动时加载 |
| **Manifest** | Connector 自描述，平台据此生成 UI 表单 schema 与 OpenAPI 元信息 |

## 5. 技术栈

| 层 | 选型 | 理由 |
|---|---|---|
| 后端语言 | Go 1.22+ | 跨平台编译、Plugin 支持成熟、单二进制部署 |
| HTTP 框架 | `gin-gonic/gin` | 中后台事实标准、middleware 生态丰富 |
| 配置 | `spf13/viper` + YAML | 支持 ENV 替换、热重载 |
| 日志 | `uber-go/zap` (JSON) | 性能与结构化 |
| 鉴权 | `golang-jwt/jwt/v5` + bcrypt | 平台 login + 可选 per-instance API Key |
| 文件监听 | `fsnotify` | 配置热重载 |
| MCP 协议实现 | 自研 + 参考 `modelcontextprotocol/go-sdk` | 控制与平台生命周期一致 |
| 数据库 SDK | `go-sql-driver/mysql`、`jackc/pgx/v5` | MySQL / PostgreSQL |
| 对象存储 SDK | `aliyun/aliyun-oss-go-sdk`、`aws-sdk-go-v2` | 阿里云 OSS / 通用 S3 (含 MinIO) |
| OpenAPI | `kin-openapi` | Swagger 文档解析与参数 schema |
| 前端 | Vite + React 18 + TypeScript + Ant Design 5 | 中后台完善、上手快 |
| HTTP 客户端 | `axios` + `react-query` | 请求/缓存 |
| 部署 | 多阶段 Docker，单镜像内嵌前端 | 拆包即用 |
| Metrics | `prometheus/client_golang` | 可观测性 |
| 测试 | `testify` + `dockertest` | 集成测试 |

## 6. 仓库结构

```
egmcp/
├── cmd/egmcp/                     # 入口 main
├── internal/
│   ├── config/                    # YAML 加载、ENV 替换、热重载
│   ├── store/                     # 实例/连接器配置持久化与并发锁
│   ├── core/                      # 路由、实例生命周期、注册中心
│   ├── mcp/                       # MCP 协议层（Streamable HTTP + SSE）
│   ├── server/                    # HTTP server、middleware、handlers
│   ├── auth/                      # 管理员认证、API Key
│   ├── audit/                     # 审计日志
│   └── connectors/builtin/        # 内置连接器
│       ├── filesystem/
│       ├── mysql/
│       ├── postgres/
│       ├── oss/                   # 阿里云 OSS
│       ├── s3/                    # 通用 S3（含 MinIO）
│       └── swagger/
├── pkg/connector/                 # 对外 SDK（Connector/Manifest 等接口）
├── plugins/                       # 第三方 .so/.dll 落地目录
├── web/                           # Vite + React + TS + AntD
│   ├── src/
│   └── dist/                      # 由 Go embed 引入
├── configs/                       # 默认配置示例
│   ├── admin.yaml
│   └── instances/*.example.yaml
├── deploy/docker/                 # Dockerfile + docker-compose
├── docs/
└── README.md
```

## 7. MCP 协议实现要点

- 同时支持 **HTTP+SSE**（legacy）与 **Streamable HTTP**（2025-03-26+ transport），客户端通过 `Accept` 自动协商。
- 实现方法：`initialize / tools/list / tools/call / resources/list / resources/read / prompts/list / prompts/get / notifications/*`。
- 工具命名空间为 `<connector>:<tool>`，避免冲突；出现在 `listChanged` 时通过 `notifications/tools/list_changed` 通知。
- 每个实例的端点 URL：`http://host:port/mcp/{slug}`（Streamable）或 `/mcp/{slug}/sse`（legacy）。
- 提供 `GET /mcp/{slug}/openapi.json`，把当前实例的可用工具导出为 OpenAPI，便于与非 MCP 客户端对接或审计。

## 8. Connector SDK（`pkg/connector`）

```go
package connector

type Connector interface {
    Manifest() Manifest
    Init(ctx context.Context, cfg json.RawMessage) error
    HealthCheck(ctx context.Context) error
    Shutdown(ctx context.Context) error
}

type ToolProvider interface { Tools() []ToolSpec }
type ToolInvoker interface {
    InvokeTool(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error)
}
type ResourceProvider interface { Resources() []ResourceSpec }
type ResourceReader interface {
    ReadResource(ctx context.Context, uri string) (ResourceContents, error)
}
type PromptProvider interface { Prompts() []PromptSpec }
type PromptRenderer interface {
    RenderPrompt(ctx context.Context, name string, args map[string]string) (PromptMessage, error)
}

// Manifest 用于 UI 与平台生成配置表单
type Manifest struct {
    Name        string         `json:"name"`        // 唯一 key，如 "mysql"
    Version     string         `json:"version"`
    DisplayName string         `json:"displayName"` // 中文友好名
    Description string         `json:"description"`
    ConfigSchema JSONSchema    `json:"configSchema"`// 生成 UI 表单
    Capabilities []string      `json:"capabilities"`// "tools" | "resources" | "prompts"
}
```

- Manifest 的 `ConfigSchema` 采用标准 JSON Schema；前端据此自动渲染 AntD Form，并标注敏感字段（`format: "password"`）。
- 不实现的接口（`ToolProvider` 等）平台会跳过对应能力。

## 9. 配置与持久化

### 9.1 平台配置 `configs/admin.yaml`

```yaml
server:
  listen: ":8080"
data_dir: "./data"
log_level: "info"

auth:
  admin_username: admin
  admin_password_hash: "$2a$10$..."      # bcrypt
  jwt_secret: "..."                       # 启动随机生成并持久化
  jwt_ttl: "12h"

instances_dir: "./data/instances"
plugins_dir: "./data/plugins"
```

启动时若文件不存在，生成默认配置并随机密码（首次启动输出密码到 stdout）。

### 9.2 实例配置 `data/instances/{slug}.yaml`

```yaml
slug: marketing
display_name: 市场数据
enabled: true
api_keys: ["marketing-prod-xxx"]
connectors:
  - type: mysql
    name: orders-db
    config:
      dsn: "${MYSQL_ORDERS_DSN}"          # ENV 替换
      readonly: true
      allow_tables: ["orders","customers"]
  - type: swagger
    name: product-api
    config:
      spec_url: "https://example.com/openapi.json"
      base_url: "https://api.example.com"
      auth:
        type: bearer
        token: "${PRODUCT_API_TOKEN}"
```

- 文件变更由 fsnotify 触发 → 校验 → `core` 重新加载指定实例 → `tools/list_changed` 推送。
- 写入使用文件锁（`flock`），避免并发丢失。

### 9.3 ENV 替换规则

- 格式：`${VAR_NAME}` 或 `${VAR_NAME:-default}`。
- 平台启动时一次性替换；失败即启动失败（fail-fast），并明确报错。

## 10. HTTP API 概览

| Method & Path | 用途 | 鉴权 |
|---|---|---|
| `POST /api/v1/auth/login` | 管理员登录，返回 JWT | 无 |
| `POST /api/v1/auth/refresh` | 刷新 JWT | Refresh token |
| `GET /api/v1/me` | 当前用户信息 | JWT |
| `GET /api/v1/connectors/builtin` | 内置连接器清单 | JWT |
| `GET /api/v1/instances` | 实例列表 | JWT |
| `POST /api/v1/instances` | 新建实例（含 connectors） | JWT |
| `GET /api/v1/instances/{slug}` | 实例详情 | JWT |
| `PATCH /api/v1/instances/{slug}` | 更新实例 | JWT |
| `DELETE /api/v1/instances/{slug}` | 删除实例 | JWT |
| `POST /api/v1/instances/{slug}/test` | 测试连接器连通性 | JWT |
| `GET /api/v1/instances/{slug}/logs` | 审计日志 | JWT |
| `POST /api/v1/instances/{slug}/rotate-key` | 生成新 API Key | JWT |
| `GET /api/v1/plugins` | 插件清单 | JWT |
| `POST /api/v1/plugins/upload` | 上传 .so/.dll（多部分） | JWT |
| `DELETE /api/v1/plugins/{name}` | 移除插件 | JWT |
| `ANY /mcp/{slug}` | Streamable HTTP（可含 `?key=...`） | API Key（可选） |
| `GET /mcp/{slug}/sse`、`POST /mcp/{slug}/messages` | legacy SSE | API Key（可选） |
| `GET /mcp/{slug}/openapi.json` | 当前实例的 OpenAPI 描述 | 无（公开） |
| `GET /healthz` | 健康检查 | 无 |
| `GET /metrics` | Prometheus 指标 | 无 |

## 11. 连接器矩阵（V1）

| Connector | Tools | Resources |
|---|---|---|
| filesystem（本地目录） | read_file, write_file, list_dir, search, delete_file | 文件夹树 |
| mysql | sql_query, list_databases, list_tables, describe_table | 各库表行数 |
| postgres | 同上 | 同上 |
| oss（阿里云） | put_object, get_object, delete_object, list, presign | bucket 列表 |
| s3（MinIO 等） | 同上 | 同上 |
| swagger | list_apis, describe_api, call_api | API 元数据 |

调研补充候选（V1 后按需采纳）：**Git、Redis、Elasticsearch、GitHub、Puppeteer（无头浏览器）、Fetch（HTTP 通用）、Brave Search、Slack、Notion、SQLite**。

## 12. 前端控制台功能

- **登录页**：用户名/密码。
- **实例列表**：每条展示 slug、状态（在线/离线）、连接器数、最后活跃时间、操作按钮（详情/编辑/测试/日志/删除）。
- **创建向导**：3 步——基本信息 → 选连接器 → 配置每个连接器。
- **连接器配置表单**：由 Connector Manifest 的 JSON Schema 自动生成。
- **日志查看器**：分页、过滤、按 instance 切换。
- **插件管理**：列表 + 上传 + 启用/禁用。
- **接入示例**：每个实例详情页提供一键复制的 JSON 片段，适配 Claude Desktop / Cursor / Cline 客户端。
- **系统设置**：管理员修改密码、查看平台版本与运行状态。

## 13. 安全与可观测

- **管理员密码**：bcrypt；首次启动随机生成并打印。
- **JWT**：HS256，TTL 12h；密钥文件持久化在 `data/secret.key`。
- **API Key**：每个实例独立数组，支持轮换；通过 `Authorization: Bearer <key>` 或 `?key=` 传递；不在 URL path 中以减少日志泄露。
- **敏感配置 ENV 替换**：DSN、Token、AccessKey 全部 ENV 注入。
- **传输**：内置 TLS 终止可选；生产建议前置 Nginx/Caddy。
- **审计日志**：每次 MCP 调用记录 instance/connector/tool/latency/status/source_ip，写入 JSONL 文件。
- **Prometheus 指标**：按 instance/connector 打标签的计数器与直方图（`egmcp_mcp_calls_total{instance,connector,tool,status}`、`egmcp_mcp_call_duration_seconds_bucket{...}`）。

## 14. Docker 打包

```dockerfile
# stage 1: build frontend
FROM node:20-alpine AS web
WORKDIR /web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# stage 2: build backend (with web embedded)
FROM golang:1.22-alpine AS go
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -o /out/egmcp ./cmd/egmcp   # CGO=1 for plugin

# stage 3: runtime
FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata
COPY --from=go /out/egmcp /usr/local/bin/egmcp
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/egmcp"]
```

- CGO=1 必开（Go Plugin 要求）。
- 镜像基础体积 ~50MB（含 Node 构建层仅在编译期出现）。
- 启动入口脚本检测 `data/`：若不存在则生成默认配置 + 随机密码，打印至 stdout；存在则正常启动。

## 15. 错误处理约定

- **错误响应结构**：`{"error": {"code": "INVALID_INPUT", "message": "...", "details": {...}}}`，HTTP status 对应语义。
- **MCP 协议错误**：遵循 MCP `error codes`（`-32700 ParseError`、`-32600 InvalidRequest` 等）。
- **连接器失败**：`HealthCheck` 失败时实例标记为 `degraded`，UI 显示但仍允许调试；`Init` 失败则拒绝激活实例。

## 16. 范围与不做的事（Non-Goals）

- 不做集群模式 / 多节点复制（V1 单实例）。
- 不做 multi-tenant 用户系统（仅管理员 + 共享 API Key）。
- 不提供 OAuth2 / SAML / LDAP 等企业级 SSO。
- 不做内置 LLM 网关（让 LLM 用 MCP 客户端即可）。
- 不打包自有 Claude/Cursor（用户自行配置）。

## 17. 测试策略

- **单元测试**：核心路由、SDK 接口实现、ENV 替换、JSON Schema 生成。
- **集成测试**：使用 `dockertest` 起 MySQL / MinIO / Postgres fixture，每个 Connector 跑端到端 MCP 调用。
- **协议测试**：使用官方 `modelcontextprotocol/go-sdk` client 自测 initialize/streamable/SSE 全流程。
- **E2E**：Playwright 跑前端关键路径（登录、建实例、复制接入片段）。

## 18. 实施里程碑

| 阶段 | 目标 | 产出 |
|---|---|---|
| M0 | 仓库骨架、单镜像启动、健康检查 | Dockerfile + hello world |
| M1 | 配置层 + admin 认证 + UI 骨架 | 登录、实例 CRUD |
| M2 | MCP Streamable HTTP + 内置 filesystem connector | 客户端能读取文件 |
| M3 | MySQL / Postgres connectors | 客户端能查询 |
| M4 | OSS / S3（MinIO） connectors | 客户端能读写对象 |
| M5 | Swagger connector | 客户端能调用 HTTP API |
| M6 | Go Plugin 加载机制 | 第三方 connector 工作 |
| M7 | 调研补充 connector（Git / Fetch 等） | 至少 2 个增量 |
| M8 | 审计日志 / 指标 / 文档收尾 | GA |

## 19. 风险与开放问题

| # | 风险 | 缓解 |
|---|---|---|
| R1 | Go Plugin 跨平台 / glibc 兼容差 | 文档说明推荐 Linux 部署；Windows 用 .dll，M1/M2 必须 Linux |
| R2 | CGO=1 引入 musl/alpine 复杂性 | 选用 glibc 基础镜像 `debian-slim` 以兼容 plugin |
| R3 | MCP 协议版本演进 | 适配层抽象为内部接口，跟随官方 SDK 升级 |
| R4 | SSE 在反向代理后易断 | 文档提示 Nginx `proxy_buffering off` 与 read timeout |
| R5 | Swagger / OpenAPI 语法差异 | 使用 `kin-openapi` 容错解析 + UI 提示 |

## 20. 参考

- [Model Context Protocol 规范](https://modelcontextprotocol.io)
- [mcp-go](https://github.com/modelcontextprotocol/go-sdk)
- [Anthropic Claude Desktop MCP 文档](https://docs.anthropic.com/en/docs/agents-and-tools/mcp)
