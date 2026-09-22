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
- 🏗 **Go 单二进制** — 无第三方依赖，`go build` 即得

## 快速开始

### 1. 构建 & 配置

> 需要 Go 1.26.5+（见 `go.mod`）。更低版本会由默认的 `GOTOOLCHAIN=auto` 自动拉取对应工具链（受限网络下可预置 `GOPROXY`）。

```bash
git clone https://github.com/guilinshanshui/phanthycode2api.git
cd phanthycode2api
go build -o phanthycode2api ./cmd/server
cp config.example.json config.json
# 编辑 config.json，设置 api_key（可留空 = 不鉴权）
```

### 2. 登录获取凭证

```bash
go run ./cmd/login -step=url
go run ./cmd/login -step=exchange -code=<授权码>
```

`cmd/login` 是**两步式**：`-step=url`（默认）生成 PKCE 授权链接并打开浏览器，把 verifier 写入 `.login-verifier` 后即退出；
浏览器完成授权 → 复制页面显示的 code → 用 `-step=exchange` 交换 token，凭证保存到 `auths/` 目录并删除 verifier。

也可直接用封装好的脚本（自动清洗 code 输入，登录后自动重启容器）：

```bash
./login.sh           # 交互式：生成 URL → 粘贴 code → 自动交换落盘
./login.sh -code=xxx # 跳过第一步，直接用已有 code 交换（需先跑过第一步）
```

### 3. 启动服务

```bash
./phanthycode2api -config config.json
```

或直接用环境变量（无需配置文件）：

```bash
P2A_LISTEN=:7864 P2A_API_KEY=your-secret-key ./phanthycode2api
```

默认监听 `:7864`，可通过 `P2A_LISTEN` 环境变量覆盖。

### 4. 验证

```bash
# 健康检查（无需鉴权）
curl -s http://localhost:7864/healthz

# 模型列表
curl -s http://localhost:7864/v1/models \
  -H "Authorization: Bearer your-secret-key"

# 聊天（非流式）
curl -s http://localhost:7864/v1/chat/completions \
  -H "Authorization: Bearer your-secret-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"DeepSeek-V4","messages":[{"role":"user","content":"你好"}]}'

# 聊天（流式）
curl -N http://localhost:7864/v1/chat/completions \
  -H "Authorization: Bearer your-secret-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"DeepSeek-V4","stream":true,"messages":[{"role":"user","content":"数到3"}]}'

# 账号池状态
curl -s http://localhost:7864/status \
  -H "Authorization: Bearer your-secret-key"
```

## 配置说明

```json
{
  "listen": ":7864",
  "api_key": "***",
  "auth_dir": "./auths",
  "state_file": "./data/state.json",
  "base_url": "https://code.phanthy.com",
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
| `cooldown.hard_credit` | `P2A_HARD_CREDIT` | `12h` | 积分不足冷却时长 |
| `cooldown.soft_rate` | `P2A_SOFT_RATE` | `60s` | 限流冷却时长 |
| `cooldown.err_threshold` | `P2A_ERR_THRESHOLD` | `3` | 连续错误阈值 |
| `cooldown.err_cooldown` | `P2A_ERR_COOLDOWN` | `10m` | 错误冷却时长 |
| `schedule.keepalive_hours` | — | `[22]` | 定时 keepalive 小时 |
| `upstream.timeout_seconds` | `P2A_TIMEOUT_SECONDS` | `120` | 上游请求超时 |

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
│   ├── upstream/        # 核心：OpenAI ↔ Anthropic 协议转换 + SSE 流式转换
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
