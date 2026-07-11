# Everything Go MCP (egmcp) · 中文文档

> [Model Context Protocol](https://modelcontextprotocol.io) 管理平台。把任意中间件——MySQL、PostgreSQL、S3/MinIO、阿里云 OSS、本地文件系统、Swagger / OpenAPI API、通用 HTTP——在几次点击之内变成符合标准的 MCP 服务，让你的 LLM 客户端（Claude Desktop、Cursor、Cline 等）直接接入。

[English (README.md)](README.md)

## 当前状态

✅ **M0–M6 已交付。** 平台现内置 7 个连接器（filesystem、MySQL、
PostgreSQL、S3 / MinIO、阿里云 OSS、Swagger / OpenAPI、通用 HTTP
`fetch`），加上一个第三方 Go 插件机制、完整的 MCP 2025-03-26
协议实现，以及一个 schema 驱动的实例创建控制台。

| ID | 标题 | 状态 |
| --- | --- | --- |
| M0 | 工程骨架 | ✅ |
| M1 | 配置 + 鉴权 + 控制台 | ✅ |
| M2 | MCP 协议 + filesystem 连接器 | ✅ |
| M3 | MySQL / PostgreSQL | ✅ |
| M4 | OSS / S3 (MinIO) | ✅ |
| M5 | Swagger / OpenAPI | ✅ |
| M6 | Go 插件加载器 | ✅ |
| M7 | Git + fetch 增量 | ⏳ |
| M8 | 审计、指标、文档、GA | ⏳ |

设计与里程碑规划见 [`docs/superpowers/specs/`](docs/superpowers/specs/)。

---

## 快速开始（Docker）

```bash
docker build -t egmcp:dev .
docker run --rm -p 8080:8080 -v egmcp-data:/data --name egmcp-dev egmcp:dev

# 在另一个终端
curl -s http://localhost:8080/healthz
# {"instance_id":"/data","status":"ok","uptime":"1s"}
```

首次启动时平台会：

1. 在 `/data/configs/admin.yaml` 写入默认配置；
2. 生成随机管理员密码与 JWT 签名密钥；
3. 把密码在容器日志里打印一次：

```bash
docker logs egmcp-dev | grep first-boot
```

打开 `http://localhost:8080/` 进入控制台。

## 本地开发

环境要求：Go 1.22+、Node 18+、（可选）Docker 用于本地夹具。

```bash
make web-install         # 安装前端依赖
make web-build           # 构建 web/dist（构建后端前必须先做这一步）
make build               # 编译后端到 bin/egmcp
make run                 # 通过 ./configs/admin.yaml 启动
# 或者：
make docker-build        # 即使本地没有 Go 也可以构建镜像
```

---

## 内置连接器

| 连接器 | 工具 | 典型用途 |
| --- | --- | --- |
| `filesystem` | `read_file`、`write_file`、`list_dir`、`delete_file`、`search`，以及 `fs://tree` 资源 | 把本地目录暴露成 MCP，沙箱化 |
| `mysql` | `sql_query`、`list_databases`、`list_tables`、`describe_table` | 只读为主的 MySQL/MariaDB |
| `postgres` | `sql_query`、`list_schemas`、`list_tables`、`describe_table` | 只读为主的 PostgreSQL |
| `s3` | `put_object`、`get_object`、`delete_object`、`list_objects`、`presign_get`、`list_buckets` | S3 兼容存储（AWS、MinIO、R2 …） |
| `oss` | 与 `s3` 同 | 阿里云 OSS |
| `swagger` | 每个 OpenAPI operation 一个工具 | 把任意 OpenAPI 3.0/3.1 文档包成 MCP |
| `fetch` | `get`、`post`、`put`、`delete`、`head` | 兜底 HTTP 客户端 |
| 第三方插件 | （自己写） | 通过 `go build -buildmode=plugin` 编译，对接 `pkg/connector` |

三类安全策略统一生效：

- **连接器级**鉴权流（`bearer` / `apiKey` / `basic` / `none`）。
- **每实例** API Key（`POST /instances/{slug}/rotate-key`），可与管理员 JWT 并存。
- **MCP 传输层**准入：`Authorization: Bearer <jwt>` 或 `X-Instance-Key: <key>`（或 `?key=` 查询参数）。

---

## MCP 传输

平台基于 [`modelcontextprotocol/go-sdk`](https://github.com/modelcontextprotocol/go-sdk)
实现了 [Model Context Protocol 2025-03-26](https://modelcontextprotocol.io)。

| 方法 & 路径 | 传输 | 鉴权 |
| --- | --- | --- |
| `ANY /mcp/{slug}` | Streamable HTTP（现代传输） | bearer 或实例 Key |
| `GET /mcp/{slug}/sse` + `POST /mcp/{slug}/messages` | 旧版 SSE | bearer 或实例 Key |
| `GET /mcp/{slug}/openapi.json` | OpenAPI 3.1 导出 | 公开 |

客户端（如 Claude Desktop）配置示例：

```json
{
  "mcpServers": {
    "my-instance": {
      "type": "http",
      "url": "http://localhost:8080/mcp/my-slug",
      "headers": {
        "Authorization": "Bearer <jwt 或实例 Key>"
      }
    }
  }
}
```

---

## 管理 REST API（全部位于 `/api/v1`）

| 方法 & 路径 | 用途 |
| --- | --- |
| `POST /auth/login` | 公开；返回 JWT |
| `POST /auth/refresh` | 续签 Token |
| `GET /me` | 管理员信息 |
| `GET /connectors/builtin` | 列出已注册连接器及其 ConfigSchema |
| `GET /instances` | 列出全部 MCP 实例 |
| `POST /instances` | 创建（校验 slug，按 slug 去重） |
| `GET / PUT / DELETE /instances/{slug}` | 读取 / 替换 / 删除 |
| `POST /instances/{slug}/test` | 对每个连接器执行 HealthCheck |
| `POST /instances/{slug}/rotate-key` | 颁发新的 API Key |
| `GET /plugins` | 列出扫描到的插件及加载状态 |
| `POST /plugins/upload` | multipart 上传（`file=…`），上限 50 MiB |
| `DELETE /plugins/{name}` | 卸载并删除插件文件 |
| `GET /healthz` / `/readyz` | 探活 |

---

## 仓库结构

```text
cmd/egmcp/                     # 入口
internal/
  config/                      # YAML 加载、ENV 替换、首启动 bootstrap
  log/                         # zap 封装（JSON 输出到 stdout）
  core/                        # router、实例注册中心
  server/                      # HTTP mux、middleware、静态资源 fallback
  auth/                        # JWT、bcrypt
  store/                       # YAML 实例存储 + fsnotify + 文件锁
  api/                         # /api/v1/* handlers（Gin）
  mcp/                         # MCP 协议适配（Streamable HTTP + SSE）
  audit/                       # 请求审计日志（M8）
  plugin/                      # 第三方 Go 插件加载（.so/.dll）
  connectors/builtin/
    filesystem/                 # 沙箱目录 MCP
    mysql/                      # go-sql-driver/mysql
    postgres/                   # pgx/v5
    s3/                         # aws-sdk-go-v2 (S3 / MinIO)
    oss/                        # 阿里云 OSS
    swagger/                    # kin-openapi
    fetch/                      # 通用 HTTP GET/POST/PUT/DELETE
pkg/
  connector/                   # Connector SDK（对插件作者稳定的 API）
  sqlconn/                     # SQL 共享策略（read-only、timeout、allow-list）
  objectstore/                 # 对象存储 Backend 接口
web/                           # Vite + React + TS + AntD 管理控制台
examples/plugin-hello/         # 第三方插件最小模板
configs/                       # admin.yaml 与实例示例
deploy/docker/                 # Dockerfile、entrypoint.sh、docker-compose.dev.yml
docs/superpowers/specs/        # 设计 + 规划文档
```

---

## 里程碑详解

### M1 — 管理控制台 + REST CRUD

打开 `http://localhost:8080/`：

1. 用首次启动时打印的管理员凭证登录。
2. 创建实例（`New instance`）：
   - **基本信息**：填 slug（将作为 `/mcp/{slug}` 路径）。
   - **类型**：选连接器类型，列表来自所有 `Manifest.ConfigSchema`。
   - **配置**：schema 驱动的表单，`password` 字段自动隐藏。
3. 在列表页管理：刷新、**Test**（对每个连接器执行 HealthCheck）、删除。
   详情页会展示配置并提供"Copy JSON"按钮，生成 `mcpServers`
   片段，可直接粘贴到 Claude Desktop / Cursor。

### M2 — filesystem 连接器

| 工具 | 说明 |
| --- | --- |
| `filesystem__read_file` | 读取 UTF-8 文本文件 |
| `filesystem__write_file` | 写入 UTF-8 文本文件（只读模式下禁用） |
| `filesystem__list_dir` | 列出目录子项（可选隐藏文件） |
| `filesystem__delete_file` | 删除文件或空目录（破坏性） |
| `filesystem__search` | 递归文件名搜索（不区分大小写） |

另外暴露一个资源 `fs://tree`，以 JSON 形式返回完整目录树。

**沙箱**：每条入参路径都会用 `filepath.Clean("/" + p)` 标准化后拼到
配置的根目录；符号链接会被解析并拒绝指向根目录之外的目标。
路径穿越、绝对路径与符号链接逃逸一律 fail-closed。

端到端验证：

| 步骤 | 结果 |
| --- | --- |
| `POST /mcp/sandbox` initialize | 200 + `Mcp-Session-Id` |
| `notifications/initialized` | 202 |
| `tools/list` | 5 个工具，带完整 JSON Schema |
| `tools/call filesystem__read_file` | 返回文件内容 |
| `tools/call filesystem__read_file ../etc/passwd` | `isError: true` |
| `GET /mcp/sandbox/openapi.json` | 合法 OpenAPI 3.1 |

### M3 — MySQL + PostgreSQL

共享的 `pkg/sqlconn` 统一施加三条策略：

- **只读**（默认 `true`）通过首关键字分类器拒绝
  非 `SELECT` / `WITH` / `SHOW` / `EXPLAIN` / `DESCRIBE` /
  `TABLE` / `VALUES` 的语句（防御层，不是 SQL 解析器）。
- **语句超时**（默认 30 秒）限制单条查询。
- **表白名单**：非空时，引用未列出表的查询会失败。表引用抽取
  采用简单正则，够覆盖大多数真实查询。

工具集：

| 工具 | 说明 |
| --- | --- |
| `sql_query` | 执行一条语句，返回 JSON 行 |
| `list_databases` / `list_schemas` | 列出可见库 / 模式 |
| `list_tables` | 列出配置库 / 模式下的表 |
| `describe_table` | 列元数据 |

本地夹具：

```bash
docker compose -f deploy/docker/docker-compose.dev.yml up -d
```

### M4 — S3 + 阿里云 OSS

共享的 `pkg/objectstore` 暴露一个 `Backend` 接口：`Stat`、`Get`、
`Put`、`Delete`、`List`、`PresignGet`、`ListBuckets`。

| 工具 | 说明 |
| --- | --- |
| `put_object` | 上传（文本或 base64） |
| `get_object` | 下载（base64） |
| `delete_object` | 幂等删除 |
| `list_objects` | 按前缀列举 |
| `presign_get` | 颁发临时 GET URL |
| `list_buckets` | 列出凭证可见的 bucket |

`deploy/docker/docker-compose.dev.yml` 自带 MinIO 夹具，监听
`localhost:9000`（root 用户 `minio` / `minio12345`）。

### M5 — Swagger / OpenAPI

给定 spec URL（或本地文件），自动为每个 path+method 生成一个
MCP 工具，并将调用转发到上游服务。

工具命名：`call__<tag>__<method>__<path>`。参数收拢为按参数名索引的
扁平对象；路径占位符在调用时替换；请求体通过 `body` 字段发送。

鉴权（连接器配置）：

| 类型 | 配置 |
| --- | --- |
| `none` | 不加任何鉴权头 |
| `bearer` | `auth.bearer` → `Authorization: Bearer <token>` |
| `apiKey` | `auth.api_key_name / api_key_value`，放在 `header`（默认）或 `query` |
| `basic` | `auth.basic_user / basic_pass`，通过 `SetBasicAuth` 发送 |

内置令牌桶限流器（默认 600 RPM，可通过 `max_rpm` 配置）防止上游
被异常请求淹没；`timeout_seconds` 限制单次调用。

### M6 — Go 插件加载器

第三方作者可以用 `go build -buildmode=plugin` 把任意 Go 文件编译
成共享库，丢进 `data/plugins/`（或通过 `POST /api/v1/plugins/upload`
上传）即成为一个一等连接器。

插件必须导出顶层变量 `Connector`（或 `NewConnector`，兼容旧版），
类型为 `pkg/connector.Connector`：

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
// 还需要实现 Init / HealthCheck / Shutdown / Tools / InvokeTool
```

构建：

```bash
# Linux / macOS
go build -buildmode=plugin -o hello.so ./examples/plugin-hello

# Windows (PowerShell)
go build -buildmode=plugin -o hello.dll ./examples/plugin-hello
```

> **限制**：插件必须与平台二进制使用同一 Go 工具链与同一 C
> 库家族（Linux glibc / Windows MSVC）。`examples/plugin-hello/`
> 提供了一个完整可用的模板。

---

## 配置

`configs/admin.yaml`（首次启动自动创建）：

```yaml
server:
  listen: ":8080"

auth:
  admin_username: admin
  admin_password_hash: "$2a$..."   # bcrypt；首次启动随机生成
  jwt_secret: "..."                 # 64-hex；首次启动随机生成
  jwt_ttl: 12h

data_dir: ./data
instances_dir: ./data/instances
plugins_dir: ./data/plugins
log_level: info
```

敏感字段支持 `${VAR}` 与 `${VAR:-default}` 环境变量引用，启动时
解析：未设置（且无默认值）时启动失败；设置为空字符串时使用
默认值。

## 许可协议

TBD