# 示例

[English](README.md) | [中文](README_cn.md)

一个 Python 服务，两种把它变成 MCP 工具的调用方式。全部标准库实现 —— 不用
`pip install`、不联网、不需要账号。

| 文件 | 作用 |
| --- | --- |
| `mcpx.example.yaml` | 网关配置：把 notes 服务发布成五个工具 |
| `python-http/server.py` | 上游服务 —— 普通 REST 接口，与 MCP 无关 |
| `python-http/client.py` | **Streamable HTTP** 客户端（POST `/mcp` + 会话头） |
| `python-stdio/client.py` | **stdio** 客户端（拉起 `mcpx stdio`，NDJSON 收发 JSON-RPC） |

两个客户端用同一份配置，这正是重点：传输变了，工具不变。

## 跑起来

终端 1 —— 上游 HTTP 服务：

```bash
python3 example/python-http/server.py --port 18081
```

终端 2 —— 网关（没有 token 会拒绝启动）：

```bash
make build                                     # 生成 bin/mcpx
# export MCPX_TOKEN=$(openssl rand -hex 24)
export MCPX_TOKEN=...                          # MCP 端点的 bearer token
./bin/mcpx serve -c example/mcpx.example.yaml
```

终端 3 —— 任选一个客户端：

```bash
# Streamable HTTP，连终端 2 的网关（--token 默认取 $MCPX_TOKEN）
python3 example/python-http/client.py --url http://127.0.0.1:18848/mcp

# stdio，自己拉起一个网关（终端 2 那个不会被用到）。
# 这里不需要 token：stdin/stdout 就是传输通道，没有网络端点。
python3 example/python-stdio/client.py --mcpx ./bin/mcpx --config example/mcpx.example.yaml
```

两者都打印一段调用记录，最后一行是 `PASS`；失败则非零退出并说明哪里不对。
结果走 stdout，过程叙述走 stderr。

也可以一条命令跑完全程（含断言与清理）：

```bash
make example-test              # 两种传输都跑
make example-test-http         # 只跑 Streamable HTTP
make example-test-stdio        # 只跑 stdio

EXAMPLE_UPSTREAM_PORT=28081 EXAMPLE_GATEWAY_PORT=28848 make example-test-http
KEEP=1 make example-test-http  # 保留进程，便于手工排查
```

## 配置在演示什么

`mcpx.example.yaml` 覆盖了配置必须说清的四件事：

- **服务在哪**：`upstreams[0].base_url` 加 `allow_prefixes: [/api]`，限定这个上游
  可以被请求的路径范围。
- **哪些接口变成工具**：每个 `operations[]` 条目是一个工具，命名
  `<upstream>_<operation>`，即 `notes_list`、`notes_get`、`notes_create`、
  `notes_delete`。
- **参数怎么绑**：`in: query` 拼到查询串，`in: path` 填进 `{id}`（URL 转义），
  `in: body` 把整个值作为 JSON 请求体发出。
- **一个通用工具**：`passthrough` 提供 `notes_request`，调用方自选方法与路径，
  但出不了 `/api`。

`mcpx check -c example/mcpx.example.yaml` 不启动任何服务就能列出这五个工具，
测试脚本第一步断言的就是它。

## 几个值得注意的点

- **端口**：配置里写死 `18081`（上游）与 `18848`（网关）。设置
  `EXAMPLE_UPSTREAM_PORT` / `EXAMPLE_GATEWAY_PORT` 时，脚本会把这两行改写到临时副本；
  Python 客户端分别用 `--port` / `--url` 指定目标。
- **开的是 bearer 鉴权**：`MCPX_TOKEN` 为空时网关直接以 `authentication misconfigured`
  退出，这是有意的 —— 因为笔误而静默开着门是更糟的失败。监听只到回环，所以 token
  可以是一次性的；客户端的 `--token` 默认取 `$MCPX_TOKEN`。测试脚本在变量未设置时
  默认填 `ci-example-token`，`make example-test` 因此仍是单条命令。
- **上游那一跳不带凭据**：`upstreams[0].auth.scheme: none`，因为 notes 服务不看
  `Authorization`；在那里写 bearer 会让每次调用都因一个没人校验的 token 而失败。
- **状态在内存**：notes 服务只种两条种子数据，重启即清空。
- **故意演示两种失败**：未知工具是 JSON-RPC 错误（`-32602`），缺必填参数是带
  `isError: true` 的结果。客户端必须能区分这两者。
- **stdio 的 stdout 纪律**：客户端 stdout 只有结果，网关的 JSON 日志留在 stderr。
  `scripts/test-example-stdio.sh` 会断言这一点 —— stdout 上多一行日志，后面每个
  协议帧都会被污染。
