# Everything Go MCP (egmcp) — 实施规划

> 日期：2026-07-02
> 依赖： [`2026-07-02-egmcp-design.md`](2026-07-02-egmcp-design.md)
> 执行节奏：按里程碑 M0 → M8 顺序推进；每完成一个里程碑即合并/打 tag。

---

## 实施总览

| ID | 里程碑 | 关键交付 | 验收 |
|----|---|---|---|
| M0 | 工程骨架 | 单镜像可启动 + 健康检查 | `curl :8080/healthz` 200 |
| M1 | 配置 + 认证 + UI 骨架 | 登录、实例 CRUD | UI 能创建/编辑/删除实例 |
| M2 | MCP 协议 + filesystem connector | 客户端能读文件 | 用官方 SDK client 能调用 |
| M3 | MySQL / Postgres connectors | 客户端能查询 | docker-compose 起 MySQL，能跑 sql_query |
| M4 | OSS / S3 connectors | 客户端能读写对象 | MinIO fixture 全链路通 |
| M5 | Swagger connector | 客户端能调 API | 用 petstore openapi 跑通 |
| M6 | Go Plugin 机制 | 第三方 connector 跑通 | 示例 plugin 加载并返回 tool 列表 |
| M7 | 调研补充 connector | ≥2 个新增 | Git + Fetch（HTTP 通用）|
| M8 | 收尾 | 审计、指标、文档 | `make test` 全过 + `docker run` demo 通 |

---

## 横切关注（贯穿各阶段）

1. **代码组织**：所有导出类型放 `pkg/connector`；内部实现放 `internal/`。任何模块不允许反向依赖。
2. **错误约定**：统一 `pkg/errs` 包，定义 `ErrCode` 与 HTTP/MCP error mapping。
3. **日志**：所有模块通过 `internal/log` 注入；MCP 入口必须含 request_id。
4. **测试**：每个包至少 1 个 happy-path 单元测试 + 1 个失败路径；引入依赖的包使用接口注入以便 mock。
5. **ENV 替换**：`internal/config` 提供 `Resolve(os.LookupEnv, raw)`，所有用户配置走它。
6. **Docker 镜像**：每个里程碑结束后构建一次镜像，确保不破。

---

## M0 · 工程骨架

**目标**：仓库目录、单镜像可启动、健康检查返回 200。

**任务**：

1. `go mod init github.com/processcrash/egmcp`，启用 Go 1.22。
2. 落地目录树：[spec §6](../superpowers/specs/2026-07-02-egmcp-design.md#6-仓库结构)（先建空包占位）。
3. `cmd/egmcp/main.go`：`--config` 参数；监听 `:8080`；返回 `/healthz` 200 JSON。
4. `Dockerfile` 多阶段（含前端占位空 dist）；保证 CGO=1 + glibc 基础镜像（`debian-slim`）。
5. `Makefile`：`make build / make run / make docker-build / make test`。
6. `README.md` 含 run/quickstart。
7. `.dockerignore`、`gitignore`、`gitleaks` 配置。

**验收**：
```bash
docker build -t egmcp:dev .
docker run --rm -p 8080:8080 egmcp:dev
curl -s localhost:8080/healthz   # {"status":"ok"}
```

---

## M1 · 配置层 + 认证 + UI 骨架

### 后端

1. `pkg/connector` 定义 [SDK 接口](../superpowers/specs/2026-07-02-egmcp-design.md#8-connector-sdkpkgconnector)。
2. `internal/config`：YAML 加载 + ENV 替换 + 默认值生成；`secret.key` 自动生成；首启动随机管理员密码并打印。
3. `internal/store`：实例 YAML 读写 + 文件锁 + fsnotify 热重载 → 触发 `core` 重建实例。
4. `internal/auth`：bcrypt 校验 + JWT 签发/解析 middleware。
5. `internal/server`：
   - `/api/v1/auth/login`、`/api/v1/auth/refresh`、`/api/v1/me`。
   - `/api/v1/instances` 全套 CRUD。
   - `/api/v1/instances/{slug}/test` 调用 Connector `HealthCheck`。
6. `internal/core`：实例 registry：每个 slug 对应 `*core.Instance`（含一组 Connector），并对外暴露 `GetToolList(slug)`。

### 前端

1. Vite + React + TS + AntD 脚手架，配置 `/api` 代理。
2. `react-query` 统一数据请求；`axios` 实例自动注入 JWT。
3. 路由：`/login`、`/instances`、`/instances/:slug`、`/plugins`、`/settings`。
4. **实例创建向导** 3 步：基本信息 → 选择 connector 类型 → 配置表单（先用内置 mock，待 M2 用 manifest 自动渲染）。
5. **详情页**：连接器卡片 + 日志 tab 占位 + "复制接入片段"按钮。
6. 设置页：修改密码。

**验收**：
- 浏览器登录、改密、CRUD 实例全部通。
- 修改 YAML 文件后实例状态自动更新（前端轮询 + 手动刷新按钮）。

---

## M2 · MCP 协议层 + Filesystem Connector

### MCP 协议

引入 [modelcontextprotocol/go-sdk](https://github.com/modelcontextprotocol/go-sdk) 作为底层实现：

- 在 `internal/mcp` 包装 SDK 的 server：
  - 实例化 server 时按 `core.GetToolList(slug)` 注入 tools/resources/prompts。
  - 注册自定义 transport handler：Streamable HTTP + legacy SSE 双栈。
- 端点：
  - `ANY /mcp/{slug}`（Streamable，遵循 2025-03-26 transport 规范）
  - `GET /mcp/{slug}/sse` + `POST /mcp/{slug}/messages`（legacy）
  - `GET /mcp/{slug}/openapi.json`（导出当前实例 tool 描述为 OpenAPI 3.1）

### Filesystem Connector

- Manifest `name=filesystem`，Tools: `read_file, write_file, list_dir, search, delete_file`。
- Resources: 目录树。
- 沙箱：实例级 `root` 配置；越界访问返回错误并审计。
- 全路径校验、符号链接拦截。

**验收**：
- 用任意官方 SDK client，写一个 stdio 包装调用 `read_file`，可读取容器内任意文件。
- 控制台点击 "复制接入片段" 生成可直接粘贴的 JSON。

---

## M3 · MySQL / PostgreSQL

### 共同抽象（`pkg/sqlconn`）

- 统一 `Open(dsn, readonly bool) (*sql.DB, error)`，注入驱动。
- SQL 类型 → JSON 友好：列名、nullable、值（`time.Time` 转 ISO8601、`[]byte` 转 base64）。
- 语句超时 + 行数上限 + 错误截断。
- 白名单：`allow_tables` 强制 SQL `FROM/JOIN` 仅命中允许表；解析失败时 fail-closed。

### MySQL

- 驱动：`github.com/go-sql-driver/mysql`。
- Tools: `sql_query`, `list_databases`, `list_tables`, `describe_table`。
- Resources: 各表行数（缓存 5min）。

### PostgreSQL

- 驱动：`github.com/jackc/pgx/v5/stdlib`。
- 工具集同 MySQL。

**验收**：
- `docker compose up` 起 MySQL 8 + Postgres 15。
- 通过 MCP 调用 `mysql:sql_query` 返回 `SELECT 1`、`list_tables`，并验证白名单拒绝越权 SQL。

---

## M4 · OSS / S3（MinIO）

### 抽象（`pkg/objectstore`）

- `Backend` 接口：`Put / Get / Delete / List / Presign`。
- 两个实现：
  - `aliOSSBackend` 基于 `github.com/aliyun/aliyun-oss-go-sdk/oss`。
  - `s3Backend` 基于 `github.com/aws/aws-sdk-go-v2/service/s3`（兼容 MinIO）。

### Connector

- Tools: `put_object, get_object, delete_object, list_objects, presign`。
- Resources: bucket 列表。
- 凭证字段标记 `format: "password"`，UI 自动隐藏输入。

**验收**：
- `docker compose up` 起 MinIO。
- 上传、下载、列对象、presigned URL 全部通。
- 阿里云 OSS 部分用 mock HTTP server 自测（OSS emulator）；接入文档中提示正式账户需自行 e2e。

---

## M5 · Swagger / OpenAPI Connector

- 使用 `github.com/getkin/kin-openapi` 解析 spec。
- 每个 path + method → 一个 tool（命名 `call_<tag>_<method>_<path>`），参数来自 spec。
- 支持 `bearer / apiKey / basic` 三种认证；通过 Manifest 配置。
- 内置限流（按 instance 配置 max_rpm），防误用。

**验收**：
- 接入 petstore openapi.json，能列出接口、能调用 `getPetById`。
- 错误响应（4xx/5xx）正确映射到 MCP error。

---

## M6 · Go Plugin 机制

### 后端

- `internal/plugin`：扫描 `plugins/` 目录，调用 `plugin.Open` 加载 `.so`/`.dll`。
- 插件导出符号 `var Connector connector.Connector`。
- 平台 UI 上传插件时，落盘到 `plugins/{name}_{version}.so`，调用 reload。
- Manifest 校验：`name` 不与内置冲突；签名校验（V1 仅记录 SHA256，可选校验）。

### 示例 Plugin（仓库内 `examples/plugin-hello/`）

- `go build -buildmode=plugin -o hello.so .`
- Manifest: 1 个 tool `hello_say` 入参 `name`，返回 `hello {name}`。

**验收**：
- 重启平台后 `/api/v1/plugins` 能列出 hello；前端可选择作为某实例 connector 并调用通过 MCP 跑通。

---

## M7 · 调研后增量（基于前 6 个里程碑反馈选定）

**候选 & 选型**：基于 `[awesome-mcp](https://github.com/punkpeye/awesome-mcp-servers)` 与市场调研，挑选 ≥2 个进入实现：

- **Git**（本地仓库读取/搜索/历史/diff）—— 强需求场景：代码 agent。
- **Fetch**（通用 HTTP GET/POST 携带鉴权）—— 兜底未提供 connector 的 API。

实施模式复用 M3/M5 经验，保持小投入。

**验收**：
- 给出 `connectors/builtin/git` 与 `connectors/builtin/fetch` 各一套单测 + 集成测试。

---

## M8 · 收尾（GA 必备）

1. **审计日志**：每次 MCP 调用写 JSONL，包含 instance/connector/tool/latency/status/source_ip/error。提供 `/api/v1/instances/{slug}/logs` 查询与简单分页。
2. **Prometheus**：在 MCP handler middleware 里记录 counter 与 histogram，按 instance/connector/tool/status 打标签。
3. **运维文档**：README + `docs/quickstart.md` + `docs/connectors.md` + `docs/plugins.md`。
4. **健康检查分级**：`/healthz`（liveness）+ `/readyz`（readiness，包括所有 active connector 的 HealthCheck 汇总）。
5. **CI**：GitHub Actions——`go test ./...`、`go vet`、`npm run lint`、`npm test`、`docker build`。
6. **Release**：发布 `v0.1.0` 镜像，写示例 docker-compose、demo GIF。

---

## 关键依赖（首次落地一次性添加）

```go
// go.mod 直接依赖
github.com/gin-gonic/gin v1.10+
github.com/spf13/viper v1.19+
go.uber.org/zap v1.27+
github.com/golang-jwt/jwt/v5
golang.org/x/crypto/bcrypt
github.com/fsnotify/fsnotify v1.7+
github.com/modelcontextprotocol/go-sdk  // MCP 协议
github.com/go-sql-driver/mysql v1.8+
github.com/jackc/pgx/v5
github.com/aliyun/aliyun-oss-go-sdk/oss
github.com/aws/aws-sdk-go-v2
github.com/getkin/kin-openapi v0.120+
github.com/prometheus/client_golang
github.com/stretchr/testify
github.com/ory/dockertest v3.10+        // 集成测试
```

前端：
```
react@18, react-dom@18
react-router-dom@6
axios, @tanstack/react-query
antd@5, @ant-design/icons
vite, typescript, @vitejs/plugin-react
eslint, prettier
```

---

## 风险与回滚（M6 风险最高）

- Go Plugin 在不同 OS / Go 版本组合下兼容差。CI 必须覆盖 Linux/amd64 与 Linux/arm64；Windows/Mac 仅开发友好。
- 若发现兼容性反复，回退方案为：M6 之后文档明确 "生产仅 Linux/amd64"，CI gate。

---

## 每个里程碑的统一 PR checklist

1. ✅ 单元测试 + 集成测试（如适用）通过。
2. ✅ `make docker-build` 通过；`docker run` 起得来。
3. ✅ README 或 docs 增量更新。
4. ✅ CHANGELOG.md 追加一条。
5. ✅ 自审：是否符合 spec；是否引入未声明的依赖。
