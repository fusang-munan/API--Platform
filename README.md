# Nebula

Nebula 是实验室正在使用的 AI API 平台，镜像版本由仓库根目录 `VERSION` 指定。学生、导师、老师共用这一套控制台：组织与项目、API Key 两级审批、供应商与模型、按倍率计费，以及 OpenAI、Anthropic、Cohere Rerank、Google Gemini 协议网关。

公网入口是 [http://106.55.103.208:25555](http://106.55.103.208:25555)。老师的项目管理在 `/teacher/projects`。代码仓库是 [fusang-munan/API--Platform](https://github.com/fusang-munan/API--Platform)。

## 现在的入口

本机前端监听 `127.0.0.1:25555`，Nginx 同源反代 `/api` 和 `/v1`。后端只监听 `127.0.0.1:25556`，不映射到公网。`frpc` 代理名 `MegaFlowAI-api-platform` 把本机 `25555` 映到 `106.55.103.208:25555`。细节见 `docs/FRP_PUBLIC_ACCESS.md`。

| 角色 | 页面 |
| --- | --- |
| 学生 | `/student/api-keys` 申请密钥，`/student/models` 模型广场，`/student/usage` 个人用量 |
| 导师 | `/mentor/reviews` 密钥管理，`/mentor/projects` 项目管理，`/mentor/call-review` 调用审查 |
| 老师 | `/teacher/projects` 项目管理，`/teacher/project-spend` 项目监控，以及学生、组织、供应商、模型管理 |

学生领到的平台密钥前缀是 `neb_sk_`，明文只在首次领取时返回一次。`/mentor/call-logs` 和 `/mentor/input-monitor` 会转到调用审查。调用审查按用户输入的提示词记一行；同一次提问里换模型，仍留在这一行，并按调用顺序列出用过的模型。新的用户提示词才另开一行。

## 北辰接入

老师可在「供应商管理」的北辰供应商行打开「同步与对账」。北辰 API 地址填 `https://api.beichenapi.com` 或其 `/v1` 地址。模型目录、倍率和可确认的单日用量由后端独立任务按凭据同步，也可以手动同步和导入报价或账单。按 Token 校内报价用于试算，现有额度按次结算。

## 技术栈

| 领域 | 现在的实现 |
| --- | --- |
| 后端 | Go，镜像用 `golang:1.26.6` 构建；HTTP 用 Gin，数据用 Ent 与 PostgreSQL |
| 前端 | Vue 3、Vite、Pinia，镜像用 Node `22.17.1` 构建，由 Nginx `1.29.1` 提供 |
| 数据 | PostgreSQL `17.5` 与 Redis `8.2.8`，数据在 Docker 卷里 |
| 会话 | JWT access token 放在内存，HttpOnly refresh Cookie 放在 Redis |
| 网关 | `net/http` 流式转发。发往上游的请求使用 `Accept-Encoding: identity` |
| 本机编排 | `scripts/compose-local.sh` 读取 `.env.local`，项目名 `nebula-api` |

`go.mod` 声明的语言版本是 Go 1.23。这台服务器的镜像构建使用本机已有的 Go 1.26.6 与 Alpine 3.22.5。

## 这台服务器怎么跑

在仓库根目录执行 `scripts/compose-local.sh`。它加载 `.env.local`，把数据库和 Redis 地址改写成 Compose 服务名，再用 `compose.yaml` 叠 `compose.local.yaml`。`.env.local` 只留在服务器上，已被 `.gitignore` 排除。

当前容器：

| 服务 | 镜像 | 作用 | 本机端口 |
| --- | --- | --- | --- |
| `frontend` | `nebula-api-frontend:0.8.6` | 控制台 | `127.0.0.1:25555` |
| `backend` | `nebula-api-backend:0.8.6` | 控制面、SSE、网关 | `127.0.0.1:25556` |
| `postgres` | `docker.1ms.run/postgres:17.5` | 业务库 | 不暴露 |
| `redis` | Redis 8.2.8 | 验证码、会话、SSE | 不暴露 |
| `maintenance` | 后端镜像 | 月度用量维护 | 不暴露 |
| `proxy-relay` | Alpine | 把宿主机代理转给后端访问上游 | 不暴露 |

应用镜像版本只由 `VERSION` 决定，Compose 不使用 `latest`。`compose.local.yaml` 把前端从默认的 `8081` 改到 `25555`，把后端从默认的 `8080` 改到 `25556`。

```bash
cd /data/home/lab_proj/MegaFlowAI/api-platform
bash scripts/compose-local.sh ps
bash scripts/compose-local.sh up -d --no-deps --force-recreate backend frontend
curl --noproxy '*' -fsS http://127.0.0.1:25556/health/ready
curl --noproxy '*' -fsS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:25555/
curl --noproxy '*' -fsS -o /dev/null -w '%{http_code}\n' http://106.55.103.208:25555/
```

迁移是一次性的 `migrate` 容器，成功后退出。`docker compose down -v` 会删掉 PostgreSQL 和 Redis 卷，只有明确要清空本地数据时才执行。

## 协作

协作者使用自己的 GitHub 账号。仓库所有者在 [Collaborators](https://github.com/fusang-munan/API--Platform/settings/access) 里添加对方，对方接受邀请后即可推送。

```bash
git clone https://github.com/fusang-munan/API--Platform.git
```

推到 `main` 之后，这台服务器会自行拉取并部署，公网仍是上面的 `25555`。协作者不需要这台机器的账号。密钥、数据库密码和 SMTP 配置留在服务器的 `.env.local`，不要写进仓库，也不要发到对话里。

服务器约每分钟看一次 `main`。提交变了才会下载源码、按 `VERSION` 构建镜像、跑迁移，并只重建 `backend` 和 `frontend`。健康检查通过后才记成已部署；失败会在下一分钟重试。只改 README 或 `docs/` 时，服务器更新检出的文档，不重建正在运行的容器。

## 配置

变量名以 `.env.example` 为准。在这台服务器上，真实值放在 `.env.local`。Go 进程自己不读 `.env` 文件，由 `scripts/compose-local.sh` 注入。至少要有：

- `DATABASE_URL`、`REDIS_URL`
- `JWT_SIGNING_KEY`、`AUTH_STATE_HASH_PEPPER`、`API_KEY_HASH_PEPPER`
- `PROVIDER_CREDENTIAL_ENCRYPTION_KEY`
- `BOOTSTRAP_TEACHER_NAME`、`BOOTSTRAP_TEACHER_EMAIL`、`BOOTSTRAP_TEACHER_PASSWORD`
- `SMTP_HOST`、`SMTP_FROM`

`PUBLIC_APP_URL` 使用公网入口 `http://106.55.103.208:25555`。容器内 `HTTP_ADDRESS` 保持 `:8080`。

## 目录

```text
cmd/server/                     控制面与网关入口
cmd/migrate/                    数据库迁移
frontend/                       Vue 控制台与 Nginx 镜像
internal/controlplane/          学生、导师、老师业务
internal/dataplane/             API Key 鉴权与协议代理
internal/usage/                 用量、额度与调用审查
docs/API_DOCUMENTATION.md       控制面与网关契约
docs/DATABASE.md                表结构
docs/FRP_PUBLIC_ACCESS.md       公网端口
docs/TESTING_AND_TROUBLESHOOTING.md
compose.yaml                    服务定义
compose.local.yaml              本机端口、数据库镜像与代理
scripts/compose-local.sh        本机启动入口
VERSION                         应用镜像版本
```

仓库里还留有 `compose.production.yaml`、Doppler 脚本，以及指向 `api.lyn91r.cn` 的部署说明。那是另一套环境。当前实验室公网只走 FRP 的 `25555`。

## 本地检查

```bash
go test ./...
go vet ./...
cd frontend && npm run typecheck && npm run test && npm run build
```

这台机器没有本机 Go 时，用已有的 `golang:1.26.6-alpine3.24` 镜像跑测试，并设置 `GOPROXY=https://goproxy.cn,direct`。
