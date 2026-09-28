# phanthycode2api

> 将 PhanthyCode CLI 的 Anthropic Messages API 转换为 OpenAI 兼容 API，Go 实现。

多账号池管理 + 冷却与错误处理 + OpenAI ↔ Anthropic 协议转换，把 PhanthyCode 上游封装成
任意 OpenAI 兼容客户端（NextChat、ChatBox、OpenCat 等）可直接接入的标准接口。

## 功能特性

- 🔑 **可选鉴权** — 配置 `api_key` 后需 Bearer token 访问（空则无鉴权，便于本地调试）
- 🔄 **OpenAI 兼容** — 无痛接入任何支持 OpenAI 格式的客户端（NextChat、ChatBox、OpenCat 等）
- 👥 **多账号池管理** — 自动轮转账号、错误计数阈值冷却、禁用、持久化状态
- 🔁 **协议转换** — OpenAI 请求/响应 ↔ Anthropic Messages API 双向转换，流式 SSE 实时透传
- 🔓 **OAuth 登录** — 半自动 PKCE 授权码流程，一键获取凭证
- ⏰ **定时 keepalive** — 保持 token 活跃，接近过期时自动刷新
- 🗺 **模型映射** — 模型名统一归一化（大小写、空白、`[1m]` 上下文后缀），历史商业名自动映射到当前公开模型 ID
- 🛡 **模型不可用不误伤账号** — 套餐未开放的模型返回 `400 model_not_allowed`，不计错误、不触发冷却
- ⚡ **显式思考开关** — 上游缺省会自行开启扩展思考（首字 10 秒+），本服务总是显式下发 `thinking`，默认关闭，可切 `auto`/`on`
- 📋 **分级日志** — `debug` / `info` / `error` 三档，默认每个请求一行摘要（含账号、模型、状态、耗时、token），排障时切 `debug` 看换号细节
- 🧵 **请求上下文透传** — 下游取消请求时立即释放上游连接
- 🏗 **Go 单二进制** — 无第三方依赖，`go build` 即得

## 快速开始

### 方式一：下载 Release（推荐）

1. 打开 [Releases](https://github.com/guilinshanshui/phanthycode2api/releases)。
2. 下载你的系统压缩包：
   - Windows：`phanthycode2api-windows-amd64.zip`
   - Linux：`phanthycode2api-linux-amd64.tar.gz`
   - macOS Intel：`phanthycode2api-darwin-amd64.tar.gz`
   - macOS Apple Silicon：`phanthycode2api-darwin-arm64.tar.gz`
3. 解压到任意目录。
4. 双击 `phanthycode2api.exe`（Windows），或运行 `./phanthycode2api`。
5. 浏览器打开管理页：

   ```text
   http://127.0.0.1:7864/admin
   ```

6. 使用默认密码 `admin123` 登录。
7. 在 **账号 → 添加账号** 里点击 **生成授权链接 → 打开链接**，登录后复制 `code`，粘贴回页面提交。

首次启动会自动生成：

- `config.json`
- `auths/`
- `data/`

程序会固定读写自己所在目录下的这些文件，所以可以放心双击运行。

### 方式二：源码构建

> 需要 Go 1.26.5+。

```bash
git clone https://github.com/guilinshanshui/phanthycode2api.git
cd phanthycode2api
go build -o phanthycode2api ./cmd/server
./phanthycode2api
```

命令行登录仍然可用：

```bash
go run ./cmd/login -step=url
go run ./cmd/login -step=exchange -code=<授权码>
```

服务默认监听 `:7864`，可用 `P2A_LISTEN` 环境变量或 `config.json` 覆盖。

### 验证

```bash
curl -s http://localhost:7864/healthz
```

## Web 管理界面

浏览器打开：

```text
http://127.0.0.1:7864/admin
```

默认密码为 `admin123`。登录后在 **设置 → 管理员密码** 里直接改即可，下次登录生效。

也可以在命令行用 `go run ./cmd/hash-password -password=你的强密码` 生成 PBKDF2 哈希后写回 `config.json`：

```json
"admin": {
  "enabled": true,
  "password_hash": "<PBKDF2 哈希>",
  "data_dir": "./data/admin"
}
```

管理页支持：

- 账号管理：生成 OAuth 授权链接、提交授权码、删除账号、手动刷新和保活
- 密钥分发：每个下游独立密钥、独立次数上限、模型白名单
- 请求日志与统计
- 图形化编辑常用配置（API Key、目录、上游地址、超时、思考模式与预算、冷却与保活），并提供原始 JSON 高级编辑

## 配置说明

```json
{
  "listen": ":7864",
  "api_key": "***",
  "auth_dir": "./auths",
  "state_file": "./data/state.json",
  "base_url": "https://code.phanthy.com",
  "log_level": "info",
  "cooldown": {
    "hard_credit": "12h",
    "soft_rate": "60s",
    "err_threshold": 3,
    "err_cooldown": "10m"
  },
  "schedule": {
    "keepalive_hours": [22]
  },
  "upstream": {
    "timeout_seconds": 120
  },
  "thinking": {
    "mode": "off",
    "budget_tokens": 4096
  },
  "admin": {
    "enabled": true,
    "password_hash": "***",
    "data_dir": "./data/admin"
  }
}
```

| 配置项 | 环境变量 | 默认值 | 说明 |
|---|---|---|---|
| `listen` | `P2A_LISTEN` | `:7864` | 监听地址 |
| `api_key` | `P2A_API_KEY` | `""` | 服务端 API 密钥（空则无鉴权） |
| `auth_dir` | `P2A_AUTH_DIR` | `./auths` | 账号凭证目录 |
| `state_file` | `P2A_STATE_FILE` | `./data/state.json` | 账号状态持久化路径 |
| `base_url` | `P2A_BASE_URL` | `https://code.phanthy.com` | 上游 API 地址 |
| `log_level` | `P2A_LOG_LEVEL` | `info` | 日志级别：`debug`（排障）/ `info`（每请求一行）/ `error`（仅异常） |
| `cooldown.hard_credit` | `P2A_HARD_CREDIT` | `12h` | 积分不足冷却时长 |
| `cooldown.soft_rate` | `P2A_SOFT_RATE` | `60s` | 限流冷却时长 |
| `cooldown.err_threshold` | `P2A_ERR_THRESHOLD` | `3` | 连续错误阈值 |
| `cooldown.err_cooldown` | `P2A_ERR_COOLDOWN` | `10m` | 错误冷却时长 |
| `schedule.keepalive_hours` | — | `[22]` | 定时 keepalive 小时 |
| `upstream.timeout_seconds` | `P2A_TIMEOUT_SECONDS` | `120` | 上游请求超时 |
| `thinking.mode` | `P2A_THINKING_MODE` | `off` | 扩展思考策略：`off` / `auto` / `on` |
| `thinking.budget_tokens` | `P2A_THINKING_BUDGET` | `4096` | 思考预算（token，1024–16384），仅 `auto` / `on` 生效 |
| `admin.enabled` | — | `true` | 是否启用 `/admin` Web 管理界面 |
| `admin.password_hash` | — | `""` | PBKDF2 管理密码哈希，空则使用默认密码 `admin123` |
| `admin.data_dir` | — | `./data/admin` | 管理数据目录 |

### 思考模式（响应速度）

上游在请求里**不带** `thinking` 字段时会自行开启扩展思考，正文首字延迟从 1~2 秒涨到 10 秒以上，
`max_tokens` 偏小时还会出现「预算全花在思考上、正文为空」。因此本服务总是显式下发该字段，
取值由 `thinking` 配置决定：

| `thinking.mode` | 行为 | 适用场景 |
|---|---|---|
| `off`（默认） | 始终下发 `{"type":"disabled"}` | 日常编码，追求首字速度 |
| `auto` | 读客户端推理档位：`none` / `minimal` / `low` 或未声明 → 关闭，`medium` 及以上 → 开启 | 想让客户端自己决定 |
| `on` | 始终开启，预算取 `thinking.budget_tokens` | 复杂推理 |

开启思考时，若客户端给的 `max_tokens` 不大于预算，会自动抬到 `budget_tokens + 1024`，
避免上游直接报参数错误。

以 Codex 接本服务为例，在 `~/.codex/config.toml` 里写：

```toml
model = "deepseek-v4.1-flash"
model_reasoning_effort = "low"
```

再把管理页的思考模式设为 `auto`，就能让低推理档位的请求自动走最快路径。

### 日志级别

服务日志写入标准错误，双击运行时同时落盘到 `logs/server.err.log`。`log_level` 控制详细程度：

| 级别 | 输出内容 |
|---|---|
| `debug` | 上面全部，外加每次换号、上游状态码、`ensure_api_key` 兜底等逐次尝试细节 |
| `info`（默认） | 启动信息、keepalive 结果，以及每个请求一行摘要：`req key=... uid=... model=... status=200 1234ms tokens=123/45 stream=true` |
| `error` | 只输出失败与异常（上游报错、写盘失败等） |

排障时把 `log_level` 改成 `debug`，或设环境变量 `P2A_LOG_LEVEL=debug`；
管理页「设置 → 基础设置 → 日志级别」也能改，保存后重启生效。
摘要行里的 `uid` 和 `tokens` 同时会写进管理页的请求日志表格。

### 可用模型

`/v1/models` 返回的即为下表模型（与上游公开目录一致），可直接作为 `model` 传入：

| 模型 ID | 上下文 | 说明 |
|---|---|---|
| `phanthy-fast` | 1.05M | 日常任务，最经济 |
| `phanthy-pro` | 1.05M | 较复杂任务 |
| `phanthy-ultra` | 1.05M | 高难度任务 |
| `glm-5.3-flash` | 1M | 日常任务 |
| `glm-5.3` | 1M | 日常任务 |
| `glm-5.2` | 1M | 日常任务 |
| `glm-5.1` | 200K | 一般任务 |
| `kimi-k3` | 1M | 较复杂任务 |
| `kimi-k2.7-code` | 256K | 日常任务 |
| `deepseek-v4.1-flash` | 1M | 日常任务 |

**可用性取决于账户套餐**：未开放的模型上游会返回 `403 model_not_allowed`。
该错误会以 `400 model_not_allowed` 透传给客户端，且**不会**让账号进入冷却（这是请求侧问题，不是账号故障）。

### 名称兼容

模型名会先归一化（去首尾空白、转小写、剥离 `[1m]`/`[2m]`/`:1m` 之类的上下文后缀、内部空白折成 `-`），
再查别名表。因此下列写法都能正常工作：

| 客户端可用写法 | 实际发往上游 |
|---|---|
| `Kimi K3`、`Kimi-k3`、`kimi-k3[1m]` | `kimi-k3` |
| `GLM 5.2`、`glm-5.2` | `glm-5.2` |
| `DeepSeek-V4` | `deepseek-v4.1-flash` |
| `Claude Opus 4.8` | `claude-opus-4-8` |
| `auto` | `phanthy-fast` |
| `gpt-5.6-sol` | `phanthy-pro` |
| `Iris-1.0`、`Zeus-1.1-pro`、`Gaia-1.2`、`Apollo-2.0`、`Metis-1.1` | 上游已下线的旧内部代号，仅作入参兼容，自动转发到对应公开 ID |

## API

### `POST /v1/chat/completions`

OpenAI 兼容。支持 `stream`（SSE 流式）、`max_tokens`、`temperature`、`top_p`，以及
`tools` / `tool_choice`（函数调用，含 `none`/`auto`/`required` 三种字符串形式）与 `stop`。
请求体上限 32 MB，超出返回 `413 request_too_large`。

客户端声明的推理档位（Codex 的 `reasoning_effort`，或 OpenAI 新格式的 `reasoning.effort`）会被读取，
用于 `thinking.mode = auto` 时判断是否开启思考；其余模式下该字段仅作参考，不下发给上游。

### `GET /v1/models`

返回上游当前公开可用的模型列表（与官网定价页一致），含 `context_length`。
套餐未开放的模型仍会列出，但调用时返回 `400 model_not_allowed`。

### `GET /status`

账号池状态概览（每个账号的 UID、状态、冷却、错误计数）。

### `GET /healthz`

健康检查（无需鉴权）。

## 鉴权机制

启用方式：在 `config.json` 设置 `api_key`（或环境变量 `P2A_API_KEY`），服务端即开启 Bearer 鉴权。

- 受保护端点（`/v1/chat/completions`、`/v1/models`、`/status`）需携带请求头：
  ```
  Authorization: Bearer <your_api_key>
  ```
- 缺失或错误的 token → `401 {"error":{"code":"invalid_api_key",...}}`
- `/healthz` **不**参与鉴权（方便健康检查与容器探针）
- `api_key` 留空（默认）→ 不鉴权，任意请求可直接访问（适合本地开发 / 内网部署）

鉴权在 `internal/server/handler.go` 的 `withAuth` 中间件中实现：根据配置的 `APIKey`
统一拦截，匹配失败直接返回 401，零外部依赖。

## Docker 部署

### 1. 构建镜像 & 启动

```bash
# 可选：编辑 docker-compose.yml 设置 P2A_API_KEY 开启鉴权
docker compose up -d --build
```

服务默认监听 `7864`，通过 `docker-compose.yml` 的 `ports` 映射到宿主机。

### 2. 凭证与状态持久化

账号凭证（`auths/`）与运行状态（`data/`）已通过卷挂载持久化，容器重建不会丢失。
**注意**：这两目录含真实 token，已在 `.gitignore` / `.dockerignore` 中排除，切勿提交。

首次使用仍需在宿主机生成凭证后再挂载：

```bash
# 宿主机本地生成凭证（两步式 OAuth），写入 ./auths
./login.sh               # 或手动两步：-step=url 然后 -step=exchange -code=xxx
# 凭证就绪后再 docker compose up，容器直接复用 ./auths
```

### 3. 自定义配置

如需完全自定义，挂载本地 `config.json` 覆盖镜像默认的 `config.example.json`：

```yaml
# docker-compose.yml（取消注释）
volumes:
  - ./config.json:/app/config.json:ro
```

### 4. 验证

```bash
# 健康检查
curl -s http://localhost:7864/healthz

# 聊天（若已开启鉴权，加 -H "Authorization: Bearer <key>"）
curl -s http://localhost:7864/v1/chat/completions \
  -H "Authorization: Bearer your-secret-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"DeepSeek-V4","messages":[{"role":"user","content":"你好"}]}'
```

### 服务端口

| 服务 | 端口 | 说明 |
|------|------|------|
| phanthycode2api | `7864` | OpenAI 兼容 API 入口 |

> 国内网络环境：Dockerfile 使用官方 `golang:1.26-alpine` 基础镜像；若拉取缓慢，可改用国内镜像源（如阿里云 `registry.cn-hangzhou.aliyuncs.com`）或配置 Docker daemon 镜像加速。

## 目录结构

```
phanthycode2api/
├── cmd/
│   ├── server/          # 服务入口：配置 → auth → pool → upstream → scheduler → server
│   │   ├── main.go
│   │   └── config.go
│   └── login/           # 半自动 OAuth 登录工具
│       └── main.go
├── internal/
│   ├── auth/            # 账号解析（三种形态）+ 原子写入
│   ├── pool/            # 账号池状态机（冷却、禁用、错误计数阈值）
│   ├── upstream/        # 核心：OpenAI ↔ Anthropic 协议转换 + 思考开关 + SSE 流式转换
│   ├── server/          # OpenAI 兼容 HTTP 服务器，带轮转与错误分类 + 鉴权中间件
│   └── scheduler/       # 定时 keepalive 任务
├── config.example.json  # 配置模板
├── login.sh             # 半自动 OAuth 登录脚本（包装 cmd/login 两步流程）
├── credit.sh            # 账号池状态 / credit 查看
├── Dockerfile           # 多阶段构建
├── Dockerfile.local     # 离线构建（复用预编译 dist/ 二进制，网络受限环境用）
├── docker-compose.yml   # 容器编排（含 healthcheck）
├── .dockerignore
├── .gitignore
└── go.mod
```

## 免责声明

本项目仅供学习和研究使用。请遵守 PhanthyCode 平台服务条款，自行承担使用风险。
作者不对任何因使用本项目产生的直接或间接损失负责。

## License

MIT
