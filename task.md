# task.md — mcpx 实施记录

> 对应 `requirements.md` **v1.1**。按阶段记录做了什么、怎么验证、与计划的偏差。
> 提交哈希以 `git log` 为准（历史经过一次整理，文中不固定引用）。

## 阶段 0 — 仓库骨架

- `go.mod`（module `mcpx`，go 1.27，唯一依赖 `gopkg.in/yaml.v3 v3.0.1`）。
- `Makefile`：`build/test/vet/fmt/check/run/stdio/tidy/clean`；`-ldflags -X` 注入
  `version.Version/Commit/Date`。
- `.gitignore`：`bin/`、`coverage.out`、`*.local.yaml`、`.env`。
- `internal/version`：`Short()` / `Full()`。
- 验收：`go build ./...` 通过。

## 阶段 1 — 配置层

- `internal/config/config.go`：`Config/Server/Defaults/Upstream/Operation/Param/Passthrough/Auth`
  结构体；`Duration` 自定义 YAML/JSON 解码；`ToolName`、`FindUpstream`。
- `load.go`：`Load` / `Decode`（`KnownFields` 拒绝未知键）、`ApplyDefaults`（幂等）。
- `validate.go`：URL、方法、路径模板、参数位置、工具名唯一、前缀、凭据变量名、
  Origin、服务端鉴权方案等全部启动期校验。
- 单测：默认值、YAML/JSON、文件加载、时长格式、45 条异常路径、直接构造校验。
- 覆盖率：**95.7%**。

## 阶段 2 — 工具与协议内核

- `internal/tool`：`Tool` 接口、`Result`、`Param`（含 `Any`）、不可变 `Registry`
  （大小写不敏感查找、游标分页 50/200）、`BuildSchema` 与 `Validate`
  （未知键/必填/七种类型/枚举）、`ScalarArg`。
- `internal/mcp`：JSON-RPC 信封（id 原样保留、通知识别）、错误码、协议类型、
  `Server.Handle`（initialize/ping/tools/list/tools/call、版本协商、错误映射）。
- 单测：协议错误码、分页、坏游标、未知工具、参数错误转 isError、通知不回复、
  caller 异常不泄漏、空注册表。
- 覆盖率：tool **94.5%**，mcp **94.8%**。

## 阶段 3 — 上游执行层

- `template.go`：路径模板编译/展开（值 `PathEscape`、拒绝 `.`/`..`/空/CRLF）、
  `ValidatePath`、`MatchPrefix`（按段、解码后匹配）。
- `client.go`：每上游 `http.Client`（连接池、超时、重定向 ≤3 且同源）、URL 拼接
  （base 路径 + 模板、`RawPath` 保真）、鉴权注入、限长读取、状态分类、结果渲染。
- `operation.go`：参数 → path/query/header/body 绑定；body 编码规则；schema 派生。
- `passthrough.go`：受限通用工具（方法枚举、前缀白名单、保留头拒绝、query/header/body 映射）。
- `redact.go`：敏感头脱敏、保留头集合、文本截断。
- 单测：请求构造、JSON body、非 2xx、截断、凭据缺失、跨源重定向、超时、取消、
  方法/路径拒绝、前缀越界与编码穿越、透传参数映射。
- 覆盖率：**90.4%**。

## 阶段 4 — 网关装配

- `internal/gateway`：`Build`（先 `Validate`，再逐上游建 client、operation、
  passthrough，最后建 registry）、`Call`（未知工具报错；参数错误与执行失败统一
  `isError`，内部错误记日志不回显细节）。
- 单测：工具注册数量与命名、非法配置、真实 httptest 上游调用、未知工具、参数错误、
  前缀越界不外发、通用错误映射、默认 logger。
- 覆盖率：**89.2%**。

## 阶段 5 — 传输层

- `internal/transport/httpsrv`：`ServeHTTP` 路由（`/healthz`、端点、404）、
  Origin 策略、Bearer 鉴权（SHA-256 常量时间）、`handlePost`（Content-Type、
  `MaxBytesReader`、会话创建/校验、通知 202、parse error → 400）、`DELETE`、
  `sessionStore`（随机 id、TTL、惰性清理）。
- `internal/transport/stdio`：`Run`（读循环在独立 goroutine，信号可立即返回）、
  `readLine`（限长）、空行跳过、EOF 正常退出。
- 单测：完整会话流程、缺失/未知会话、DELETE、401 与 `WWW-Authenticate`、Origin
  默认/白名单/通配、413、415、405、`/healthz`、读写失败、无换行尾行、信号取消。
- 覆盖率：httpsrv **90.0%**，stdio **90.4%**。

## 阶段 6 — CLI

- `cmd/mcpx`：命令分发（默认 `serve`）、`-c/--config`、`-v`、`MCPX_CONFIG`；
  `serve`（令牌解析、监听、监听失败退出 1、信号 → `Shutdown`）、`stdio`、
  `check`（打印 listen/upstreams/tools）、`version`、`help`。
- 单测：分发与退出码、check 三种结果、缺凭据、坏监听地址、stdio 真实 stdin/stdout、
  `serverToken`、`defaultConfigPath`。
- 覆盖率：**83.6%**（`runServe` 的成功路径依赖真实监听，未在测试里拉长生命周期）。

## 阶段 7 — 测试加固（覆盖率 75% → 91.8%）

| 包 | 加固前 | 加固后 | 主要补充 |
| --- | --- | --- | --- |
| config | 66.2% | 95.7% | 文件加载、时长 JSON、header 连字符、45 条校验异常、直接构造 |
| upstream | 69.8% | 90.4% | 三种鉴权、保留头防护、结果映射、脱敏、透传映射与拒绝 |
| tool | 73.3% | 94.5% | 访问器、分页边界、七种类型、枚举、标量化 |
| mcp | 84.4% | 94.8% | 默认身份、非法 params、caller 异常、空注册表 |
| gateway | 81.1% | 89.2% | 通用错误映射、默认 logger |
| httpsrv | 83.3% | 90.0% | 会话错误、Origin 通配、415、healthz 无注册表 |
| stdio | 82.7% | 90.4% | 读写失败、无换行尾行、空行 |
| cmd | 0% | 83.6% | 命令分发、check、serve 失败路径、stdio 往返 |
| version | 0% | 100% | `Short` / `Full` |
| **合计** | **~75%** | **91.8%** | 116 个测试函数 |

新增 `make cover` / `make cover-html`；README 补充覆盖率命令与测试约定。

## 阶段 8 — 文档与 Git

- `README.md`：定位、快速开始、字段表、参数绑定、两种接入方式、安全模型、
  错误语义、开发命令、明确不做的事。
- `AGENTS.md`：架构、硬性约定、测试要求、注意事项、需求状态。
- `requirements.md`（本契约）、`task.md`（本文件）。
- Git：`git init -b main`，提交「feat: add MCP gateway…」与
  「test: raise statement coverage to 92%」。

## 阶段 9 — 容器化

- `Dockerfile`：两阶段。`golang:1.27-alpine` 构建静态二进制（`CGO_ENABLED=0`、
  `-trimpath -s -w`、`-X` 注入 `VERSION/COMMIT/DATE`）；`alpine:3.21` 运行时安装
  `ca-certificates tzdata wget`，建 uid 10001 的 `mcpx` 用户，二进制放 `/app/mcpx`，
  配置默认 `/etc/mcpx/mcpx.yaml`，`EXPOSE 8848`，`HEALTHCHECK` 打 `/healthz`。
  构建参数沿用 agenthub 的约定：`BUILD_PROXY`、`GOPROXY`（默认 goproxy.cn）、
  `ALPINE_MIRROR`，`go mod download` 带无代理回退与可执行的失败提示。
- `deploy/mcpx.docker.yaml`：容器配置，监听 `0.0.0.0:8848`（容器内 `127.0.0.1` 不可达），
  bearer 鉴权取 `MCPX_TOKEN`，上游指向 compose 的 `demo` 服务。
- `docker-compose.yml`：`mcpx`（构建、只读根文件系统、`cap_drop: ALL`、
  `no-new-privileges`、`tmpfs /tmp`、端口默认只绑 `127.0.0.1`、配置只读挂载）+
  `demo`（`hashicorp/http-echo`，让新栈立刻有一个可调用工具）。
  缺 `MCPX_TOKEN` 时进程打印 `authentication misconfigured` 后退出，而 `down/ps/logs`
  不依赖该变量。
- `.dockerignore`：排除 `.git`、产物、文档与测试，只保留 `go.mod/go.sum`、`cmd`、
  `internal`、`deploy`。
- `.env.example`：`MCPX_TOKEN`、`MCPX_BIND`、`MCPX_PORT`、`MCPX_CONFIG_FILE`、
  `DEMO_TOKEN`。
- `Makefile`：新增 `docker-build`（`--network=host` + 版本戳）、`compose-config`、
  `compose-up`、`compose-down`。
- 验证（本机 Docker 29.9 / Compose v5.6）：`docker build --check .` 无告警；
  `docker build` 成功；`docker compose up -d --build` 后 `/healthz` 返回
  `tools:1`，`tools/call demo_hello` 返回 `hello from the mcpx demo upstream`；
  `docker compose down` 正常清理。

## 测试期发现并修复的问题

1. **header 参数名不允许连字符**：`X-Trace` 这类标准写法被参数名校验拒绝。
   修复：按位置选文法（header 用 HTTP token），并对 header 名做大小写不敏感去重。
2. **`/` 前缀永不匹配**：`MatchPrefix("/x", ["/"])` 返回 false，导致无法声明
   "整个上游"。修复：`base == ""` 视为命中全部，并补用例锁定。
3. **stdio 阻塞读无法被信号打断**：读循环移入 goroutine，`Run` 在 `ctx.Done()`
   时直接返回；进程可及时退出。
4. **`tools/list`/`tools/call` 在空注册表时 panic**：改为返回 `-32603`。

## 偏差与说明

- 传输包目录为 `internal/transport/httpsrv`（包名 `httpsrv`），避免与标准库
  `net/http` 同名带来的别名噪音；行为与计划一致。
- 上游 `timeout` 与 `max_response_bytes` 在 `ApplyDefaults` 阶段继承 `defaults`，
  因此运行时不再判空；`Validate` 中对应的零值检查属于防御性分支。
- `http.MethodHead` 在数据面被允许，但 `GET/HEAD {path}` 在 MCP 端点上固定 405。
- compose 未使用 `${MCPX_TOKEN:?}`：那会让 `down/ps/logs` 也因缺变量失败；代价是缺
  token 时容器进入重启循环（日志有明确原因），用 `docker compose logs mcpx` 即可定位。

## 后续待办

- [ ] 可选指标端点（`/metrics`）与请求耗时直方图；继续保持零第三方依赖需自实现。
- [ ] 上游重试策略（仅幂等方法、有上限、可关闭）与熔断。
- [ ] 配置 schema 的 `$ref`/默认值文档生成（当前 README 手写）。
- [ ] 覆盖 `runServe` 成功路径与优雅退出（需要可注入监听器或在测试里发送信号）。
