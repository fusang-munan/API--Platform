# Nebula 测试与故障排查

学校证书通知使用 `Lab backend TLS integration` 中的本地 SMTP 接收器验收。通过维护 HTTPS 提交单份已批准申请，验证邮件只含实际版本、入口和监听证书的指纹，重复请求和后端重建均不会再发。202 表示已处理公开请求；若尚未收信，请先检查收件箱和垃圾箱、学校发件地址、批准的版本及到期时间。勿重复使用注册、邀请或密码找回流程查询投递。

## 查询某一笔学生扣分

保存学校网关返回的 `X-Request-ID`，用发起调用的学生账号查询 `GET /api/v1/student/usage/calls/{request_id}`。404 时核对账号、请求编号和实际供应商；本人重复使用编号也会得到 404。收到 2xx 推理响应后仍可能暂时没有北辰流水，保留原编号稍后重查即可。一次超时也可能已入账，应先读同一编号的回执。金额为 `null` 表示该项尚无数字可核对。

执行 `go test ./internal/usage ./internal/api/http -run StudentCallReceipt -count=1` 检查回执、输入限制和匿名鉴权。配置隔离 PostgreSQL 后，`go test ./internal/usage -run TestBeichenLedgerPostgres -count=1 -v` 同时覆盖本人范围、编号歧义及精确金额。

## 学校网页能打开，维护用 HTTPS 握手失败

教师在模型页确认已发现的模型、当前倍率与启用权限，在项目中给学生分配额度、查看开销。维护者可从学校已核实的 HTTPS 入口读取收件公钥，随下一次 GitHub 自动部署交付加密的渠道 Key。学校的北辰网页会话在可信 HTTPS 教师页保存，点「测试抓取」即可核对服务器读取结果。

0.13.0 的学校 HTTPS 应能匿名读取 `/teacher/providers`、`/student/api-keys`、网页引用的 JS/CSS 和 `/.well-known/nebula-release.txt`。使用已独立核实的学校 PEM 证书执行 `curl --cacert school.pem https://106.55.103.208:25555/teacher/providers` 应返回官方控制台 HTML。同样请求 `/api/v1/teacher/providers` 时，匿名应返回 401。页面返回 502 而 API 能访问时，检查前后端均已更新，且学校前端容器的静态服务正在 8082 监听。浏览器报告未知签发机构时，需解决该设备对学校证书的信任，再录入密码或密钥。

0.12.0 起实验室配置默认关闭单次证书邮件批准。后续获同意的通知可在发布时配置 `LAB_CERTIFICATE_NOTICE_REQUEST_SHA256` 和 `LAB_CERTIFICATE_NOTICE_EXPIRES_AT`，两项为空时通知路径返回 404。维护 HTTPS 和正常登录可以继续使用。

学校自动更新由校内程序执行。先访问 `/.well-known/nebula-release.txt`。0.10.3 起前端镜像会返回构建时的版本；旧镜像的网页回退规则会返回 HTML。该值只能用来识别镜像。HTTPS 的证书和受保护接口须另行检查。

0.10.2 的学校前端通过宿主机目录加载启动文件。若部署程序移动源码或该目录仅允许宿主用户读取，容器里的用户 10001 无法启动 Nginx。0.10.3 已把两个启动文件随镜像打包，并去掉这个目录挂载。在隔离的 Linux Docker 环境运行 `bash scripts/test-lab-port.sh`，可检查学校 Compose 的启动参数、镜像版本、同端口 HTTP 与 HTTPS、错误证书拒绝以及更换后端后的连接恢复。若版本已更新而仍收不到证书，还需区分校内后端证书卷、8443 监听与公网转发。

PR 中的 `Lab backend TLS integration` 在独立 Linux 环境启动真实迁移和后端程序，使用临时 PostgreSQL、Redis 和证书卷。它检查第一次启动时的证书文件权限、重建后的证书一致性，以及无凭据访问教师接口时 HTTP 和 HTTPS 的 401 响应。

## 登录页星轨夜视觉验证

0.8.6 根据用户要求将代码绘制的 Logo 替换为内置 imagegen 生成的几何 N 字标素材 `nebula-n-generated.png`，不使用手绘 SVG。所有入口统一引用其 PNG 尺寸衍生图（页面 128px、favicon 32/192px、Apple 180px），仅缩放导出，不重新绘制图形。验证所有图片返回 image/png，32px 下仍可辨识，页面不再引用 nebula-star.svg。上一版移除的英文引导标签保持删除。原图和生成提示词保留在仓库，可据此追溯素材来源。

0.8.6 验证：类型检查、生产构建、四种屏宽、模拟登录和身份选择、星球动画、减少动态效果及 PNG 图标响应检查通过。前端发布镜像为 `nebula-api-frontend:0.8.6`。

0.8.5 移除登录卡片的英文引导标签；新品牌源文件为 `frontend/public/assets/brand/nebula-star.svg`，使用雪青紫星核、倾斜轨道与卫星光点。登录、其他认证页、会话加载页和控制台使用同一 SVG，浏览器提供同源 SVG、32px PNG fallback 和 180px Apple 图标。验证登录页不含 `YOUR NEXT CHAPTER`，所有品牌图片成功加载，HTML 图标链接指向新素材且返回正确 MIME 类型；新文件名避免沿用旧 favicon 缓存。SVG 缩小至 16/32px 时应仍可识别星核与轨道。

0.8.5 验证结果：类型检查、生产镜像构建、四种屏宽布局、模拟登录交互、星球动画及减少动态效果检查通过；新 SVG/PNG/Apple 图标均返回 200 和正确 MIME，注册页品牌素材加载通过。前端单独更新为 `nebula-api-frontend:0.8.5`。32px 和 180px PNG 由同一 SVG 通过 Chromium 按目标尺寸透明截图导出。

0.8.4 登录页移除治理标语、功能标签及页脚文案，品牌图形使用与 favicon 相同的高清素材。`LoginPlanet.vue` 通过 CSS 实现球面转动、光晕呼吸、星环倾斜与光点公转；无需动画依赖和逐帧 JavaScript。浏览器已确认动画前后球面 transform 与光点 offset-distance 变化；减少动态效果模式下所有星球动画停止。品牌图片加载成功且 src 为 `/assets/brand/favicon-512.png`，移除的文案不再出现在页面。类型检查、生产构建和四种宽度的布局与模拟交互检查通过，前端单独发布为 `nebula-api-frontend:0.8.4`。

登录页使用独立 `frontend/src/components/LoginShell.vue`，保留邮箱密码、密码显隐、多身份选择和原有认证调用。在 frontend 执行 `npm run build` 与 `npm run test`；浏览器检查 1440px、768px、390px 和 320px 宽度，确认无横向溢出、表单与文档入口可用、键盘焦点清晰。启用 `prefers-reduced-motion: reduce` 时不播放星光与入场动画。不使用真实账号测试。

0.8.3 验证：类型检查及 Docker 生产构建通过；上述四种宽度无横向溢出，模拟 API 下密码显隐、三身份展示、返回登录、减少动画及无 JavaScript 运行错误检查通过。截图保存在 `docs/reports/login-night/`。前端单独更新到 `nebula-api-frontend:0.8.3`。

若登录页仍显示旧样式，检查页面加载的带 hash JS/CSS 是否与最新 frontend/dist/index.html 一致，并重新部署前端静态产物。若其他认证页发生视觉变化，检查是否误将独立 LoginShell 用于其他页面；公共 AuthShell 保留原有用途。此变更不涉及 API 或数据库契约。

本次验证中，现有 `auth.test.ts` 的会话恢复断言和 `client.test.ts` 的导师重复指派文案断言失败；恢复改造前的 Login.vue 后仍可复现。前者的 mock 未覆盖当前 bootstrap 的 `/me` 读取，后者的期望文案与现实现不一致，需在对应认证/API 工作中同步。排查命令：`npm run test -- src/store/auth.test.ts src/api/client.test.ts`。本机 node_modules 若由其他用户拥有，Vue 类型缓存可能报 EACCES；可复制前端到当前用户可写的临时目录后执行构建，避免改动共享依赖目录权限。

## 1. 文档职责

本文档用于统一测试入口和支持排查口径，避免通过反复问答才能定位常见问题。新增、修改或删除任何功能时，必须在同一 Task 更新：

- 相关测试范围和验证命令；
- 用户可观察到的失败现象；
- 已知根因；
- 解决方案；
- 从低成本到高成本的排查步骤。

本文档不得记录真实邮箱验证码、JWT、refresh token、API Key、供应商凭据、加密密钥、数据库 DSN 或用户隐私数据。日志和工单只允许使用 request ID、资源 ID、Key prefix 和脱敏邮箱。

## 2. 标准验证流程

### 2.1 静态与单元验证

```powershell
go generate ./internal/infrastructure/db/ent
go test ./...
go vet ./...
cd frontend
npm run typecheck
npm run test
npm run build
```

当前自动化测试覆盖：

- 模型倍率解析、项目/Key 月度 credit 聚合、调用日志与输入监控 DTO；新增 PostgreSQL migration、额度门禁、失败返还和权限集成测试应在发布前执行。

### 限额、调用日志与输入监控

- 学生 `/student/usage`、导师项目用量、老师项目花费必须通过真实 HTTP JSON envelope 验证 snake_case 字段，禁止只构造前端对象。Go usage DTO 缺少 `json` tag 时，接口会返回 `ID/Quota/Models`，前端按 `id/quota/models` 读取后表现为空白或 `undefined.map`。
- 模型广场展示 `credit_multiplier`。老师修改已有倍率时要求 `multiplier_change_reason`，首次设置默认倍率时可省略原因。Key 申请验证申请额度，导师批准验证项目分配额度。
- 老师模型向导只通过顶部节点导航：填写模型 ID 和名称后第二节点解锁，第一步不显示“下一步”。进入第二步必须立即显示一次“一键填充”询问；选择否后保留描述、类别、文本模态、128K/128K/4K 容量和 1.00x 倍率默认值。选择是时模型 ID 与上游模型名默认来自第一步；Probe 必须自动请求 `/v1/models`，将真实 description、tags、model type、endpoint types 和按优先级推断的容量写回第二步，成功后关闭子弹窗且不渲染响应 body。Binding 加号和编辑按钮必须打开 Portal 居中弹窗。
- 导师批准的测试验证项目月 bucket 更新同时绑定 `project_id` 与当前上海月份。SQL 参数数量不一致时 pgx 拒绝执行并回滚。接口返回的 `request_id` 可用于定位该事务的错误。
- 调用日志无数据时先区分真实空集和渲染失败：检查 `/mentor/call-logs` 的 `items`，再通过 fake upstream 发一笔测试调用，确认 `gateway_calls`、`monthly_usage_cube` 和适用协议的 `monitored_inputs` 同时更新。
- 生产 Playwright E2E 仅使用专用测试身份与测试 Key；凭据不得写入源码、报告或截图。额度/倍率写操作只针对专用测试资源，并保留永久审计。
- 导师项目详情的视觉验收必须检查：两张环图都有可见颜色图例；额度分配按成员的全部 Key 额度之和分段；成员明细采用横向卡片，每成员一张，左侧为成员身份，中间纵向排列多个 Key 的额度轨道/已用填充/右侧百分比，右侧显示该成员的 0x 免费模型请求次数表并保留 Key 状态；逐模型验证所有成员 `free_models.calls` 之和等于项目顶层 `free_models.calls`；窄屏应依次降为两列和单列。老师项目花费必须是按项目分行的表格，行内提供 mini-donut，展开后显示按模型拆分且带可见图例的大图。学生个人用量的每个 Key 环图也必须有可见模型/剩余额度图例。
- 所有 `CreditDonut` 环图的 tooltip 必须限制在各自图表视区内；将鼠标移至左右边缘扇区时，文本不得落到图表容器外而被父级裁切。该规则同时覆盖导师项目额度分配/实际开销、老师项目花费和学生个人用量图。
- 导师调用日志和输入监控的资源筛选必须为同一套级联下拉：项目仅列负责项目；选择项目后成员、Key、模型按项目收窄；选择成员后 Key、模型继续收窄；选择 Key 后模型按白名单收窄；修改上级必须清空全部下级；任一下级为空表示当前上级范围内全部。关键词和日期时间不是枚举，分别保留文本框与日期时间控件。验证只有 0x 调用的已撤销 Key 仍可作为当月筛选项。

- 测试范围：模型倍率定点解析、上海时区月桶、Key/项目额度门禁、供应商故障切换、调用状态结算、日志分页、导师项目范围和提示词访问审计。
- 正常路径：请求解析模型后创建 pending call，成功结算 charged 与月度聚合；提示词仅在 charged/outcome_unknown 后可见。
- 权限/安全边界：老师只看项目按模型聚合，导师只看当前负责项目的调用日志和输入监控；完整提示词读取必须先成功写访问审计。
- 并发或事务边界：项目月桶和 Key 月桶必须在固定锁序下条件更新；重复 finalize/recovery 不得重复计费；撤销与 reserve 使用同一事务规则。
- 常见现象：额度耗尽返回 `429`；日志出现 `outcome_unknown`；输入监控列表无全文。
- 根因：Key/项目月桶已达到额度、客户端在上游发送后中断、或输入文本仍需点击详情读取。
- 解决方案：等待北京时间下月刷新或使用 0 倍率模型；按 request ID 检查调用尝试；在导师项目范围内打开详情。
- 排查命令或观察点：`go test ./...`、`go vet ./...`、`cd frontend; npm run typecheck`；查看 `gateway_calls`、`gateway_call_attempts` 的脱敏状态和 `monthly_usage_cube` 聚合。
- 不得记录的敏感信息：完整 API Key、供应商凭据、数据库 DSN、refresh token、JWT、提示词原文和上游原始响应 body。

- API Key 合法/非法状态迁移及驳回、撤销原因规则；
- bcrypt、JWT audience/issuer、API Key HMAC、AES-GCM 防篡改；
- 标准网关路由与旧路由拒绝；
- 控制面完整路由注册，以及不存在 DELETE 和旧网关前缀；
- 供应商 URL 拼接、公开模型 ID 到上游模型名重写；
- JSON/multipart body 重放、文件内容保真和请求大小限制；
- Adapter 协议隔离，以及 Images/Audio/Videos/Realtime/Moderations 的 route-to-adapter 映射；
- Codex Responses HTTP/compact/WebSocket 的未知字段、加密 compaction item、prompt cache、service tier、`OpenAI-Beta` 与 model 改写保真；
- Realtime client secret 的 `session.model`、WebRTC calls multipart `session.model` 改写和 SDP 保真；
- Video resource 创建后的 Redis binding 关联，以及查询、content、remix 返回同一上游；
- 控制面结构化错误码到 HTTP 状态码映射；
- 安全配置长度、加密主密钥、开发 Cookie 行为，以及固定测试账号 Secret 的成套解析和角色校验。
- 前端类型检查、生产构建、关键 API client 行为，以及初次加载/刷新状态分离、并发请求计数、相同操作去重和可访问骨架渲染。
- 生产 Dockerfile 的 BuildKit 契约：部署显式启用 BuildKit，`go mod download` 使用持久化 module cache，三个 Go 二进制的串行编译同时使用 module cache 和 build cache。
- 导师候选游标编解码、学生新模型元数据校验与并发复用规则、nullable 模型字段三态解析、老师驳回进度映射、模型 ID 去重和 1–100 数量校验。

### 2.2 本地依赖检查

```powershell
go version
docker version
docker compose version
doppler --version
doppler configure get project --plain
.\scripts\compose.ps1 config --quiet
```

PostgreSQL 和 Redis 按设计不映射宿主机端口，不能再用 `Test-NetConnection localhost -Port 5432/6379` 判断它们。使用 `.\scripts\compose.ps1 ps` 查看容器健康状态，并通过 backend readiness 统一判断依赖。

生产测试账号凭据保存在 Doppler `nebula-api/prd`，每个角色各三组：`TEST_STUDENT_1..3_{NAME,EMAIL,PASSWORD}` 和 `TEST_MENTOR_1..3_{NAME,EMAIL,PASSWORD}`。生产部署要求六组三元组全部存在，并将其作为 backend 运行时环境注入；服务启动时按邮箱幂等创建缺失用户。这些值不写入仓库或 backend 日志。已有同邮箱、同角色用户不会更新密码或状态；同邮箱角色冲突会导致 backend 启动失败。

### 2.3 数据库初始化

```powershell
.\scripts\compose.ps1 up -d --build
```

该命令使用 Doppler `nebula-api/dev_personal` 的真实配置，创建/更新 Docker 命名卷中的本地数据库结构。启动脚本拒绝外部数据库 host，防止把生产 DSN 带入本地迁移。

### 2.4 Docker 验证

```powershell
.\scripts\compose.ps1 ps
.\scripts\compose.ps1 images
Invoke-RestMethod http://127.0.0.1:8080/health/live
Invoke-RestMethod http://127.0.0.1:8080/health/ready
.\scripts\compose.ps1 logs --tail 100 backend
```

本地 Compose 期望：`postgres`、`redis`、`backend` 为 healthy，`frontend` 为 running，`migrate` 为 exited (0)。生产 Compose 使用 `compose.production.yaml`，期望 `postgres`、`redis`、`backend`、`maintenance`、`model-catalog`、`cloudflared` 运行，`migrate` exited (0)，且不运行 frontend。健康响应不得包含 DSN、secret 或异常文本。

### 2.5 生产部署验证

push 到 `main` 后，在 GitHub Actions 的 `Deploy production` workflow 中确认源码已同步到服务器、第三方镜像依次拉取、单核 Go 构建、分阶段 service 启动、显式 migrate 和两个 HTTP 健康检查均成功。服务器端只读检查：

```bash
cd /opt/nebula-api
docker compose --project-name nebula-api --file compose.production.yaml ps
curl --fail http://127.0.0.1:8080/health/ready
curl --fail https://api.lyn91r.cn/health/ready
```

期望 `/opt/nebula-api` 保存当前部署源码，`postgres`、`redis`、`backend`、`maintenance`、`cloudflared` 正在运行，`migrate` 成功退出，内部和公网 readiness 均返回 200。GitHub 日志不得出现 SSH 密码、Doppler token、DSN 或应用 secret。公网访问通过 Cloudflare Tunnel 的 `https://api.lyn91r.cn`，production 登录和 API Key 传输必须经过 HTTPS。

Vercel 项目必须将 Root Directory 设置为 `frontend`。前端部署完成后，直接请求 history 路由应返回控制台 HTML，而不是 Vercel 404：

```powershell
(Invoke-WebRequest -UseBasicParsing https://www.lyn91r.cn/login).StatusCode
(Invoke-WebRequest -UseBasicParsing https://www.lyn91r.cn/teacher/key-reviews).StatusCode
```

两项均应返回 `200`，随后由 Vue Router 和认证守卫决定页面内容及登录跳转。

### 2.6 首期业务验收

1. 学生和导师分别完成验证码注册，老师由 bootstrap 或邀请激活。
2. 老师创建组织和项目，并将导师加入组织。
3. 导师申请项目，老师批准。
4. 学生填写组织、项目、Key 名称和申请额度，提交零模型申请。
5. 负责该项目的导师批准后，状态从 `pending_mentor` 变为 `approved`。
6. 核对该项目的当月额度分配和审批审计。
7. 确认学生已自动加入组织和项目。
8. 学生首次领取，取得一次性明文并转为 `active`；重复领取必须失败。
9. 使用 Key 调用 `GET /v1/models` 确认目录为空。添加模型后用假上游验证原 Key 可调用；移除后验证原 Key 无法再调用该模型。
10. 导师撤销 Key，后续网关调用必须失败，但学生成员关系保留。

### 2.7 控制台人工验收

1. 分别用学生、导师、老师账号登录，确认默认落点、侧栏权限、手机导航抽屉和退出登录；导师从“审核密钥”点击“项目管理”后必须保留完整工作台并显示项目列表或明确空状态。
2. 学生选择组织后确认停用/无导师项目可见但禁选；切换组织确认会清空后续草稿。
3. 没有生效 Key 时，从模型广场进入申请，填写组织、项目、Key 名称和额度。
4. 负责导师批准零模型申请，并验证带原因的驳回。
5. 学生观察 SSE 状态变化：只有已有 Key 的状态真正变化时自动打开该 Key 详情；首次进入、普通刷新、SSE 重连和新提交不会自动弹窗。领取时验证未复制关闭二次确认、复制失败手动选择及关闭后 secret 清空。
6. 用生效 Key 在模型广场点击「添加到我的 Key」，核对该模型显示「已同步」，并出现在「申请密钥」页的「已授权模型」列表。选择不同项目的 Key 时，核对授权列表与所选 Key 对应。
6. 老师验证导师候选搜索分页、组织/项目启停、凭据独立替换、模型 nullable 数值和 Binding 编辑。
7. 缩窄至 375px 宽度并仅使用键盘完成认证、申请、审核和弹层关闭；开启 reduced motion 后确认无位移、缩放、旋转或 shimmer 动画，但加载文字、禁用态和错误信息仍然可见。
8. 对每个角色依次打开所有侧栏页面：GET 期间应显示顶部加载进度和数据骨架；使用局部 `LoadingRegion` 的复杂列表在初次加载时不得先闪现误导性的空状态，刷新时继续保留已有内容；登录、注册、退出及使用局部 pending 的写操作按钮应显示处理中并禁止重复提交。

## 3. 常见问题矩阵

### 学生 Key 模型授权

设置 `NEBULA_TEST_POSTGRES` 为隔离的本机测试库，运行 `go test ./internal/dataplane ./internal/infrastructure/db -run 'TestStudentKey(ApplicationWithoutModels|ModelAuthorization|ModelRemovalMigration)Postgres' -count=1`。测试覆盖零模型申请、批准和领取，九条旧授权中两条进入默认目录，待验证告警、原 Key 即时生效、越权拒绝、审计失败回滚及迁移保留历史。Key 列表和模型广场都验证最近探活时间，包括探活失败和 `null`。调用使用假上游。

- 模型显示维护中时，核对模型状态、供应商、凭据、目录条目和最近探活结果。
- 用同一把 Key 分别请求 `GET /v1/models` 和 `GET /v1/models?include_unavailable=1`。前者是客户端默认目录，后者可核对维护中的授权。
- 并行运行全量 PostgreSQL 用例时，如果出现 `citext` 类型不存在或 `pg_extension_name_index` 冲突，先在隔离测试库的 `public` schema 执行 `CREATE EXTENSION IF NOT EXISTS citext WITH SCHEMA public`，并以相同方式启用 `pg_trgm` 和 `pgcrypto`。再运行 `go test ./...`。

### 北辰接入验收

0.29.0 渠道回归需设置 `NEBULA_CHANNEL_TEST_POSTGRES`，指向隔离的本机 `nebula_test_*` 数据库。迁移包验证 0071 升到 0072 后的独立渠道登记、旧 Gemini 恢复和官网 Key 归属。从仓库根目录运行以下命令，使用 Go 的测试覆盖文件载入官网身份核对用例。

```powershell
go test -p 1 -overlay internal/infrastructure/db/migrations/testdata/beichen_channel_tests.overlay.json ./internal/beichen ./internal/infrastructure/db/migrations -run 'TestBeichen(Channel|TeacherPanel|IndependentChannel|Restores|UpgradeChain)' -count=1
go test ./internal/dataplane ./internal/usage -run 'TestBeichenGemini' -count=1
```

若教师登记或更换 Key 被拒绝，先检查可信 HTTPS 入口，再核对活动渠道登记和官网 Key 归属。收到 Gemini 归属待确认时，核实原凭据后在恢复请求中传入 `confirm_retired_channel`。若原生目录或探活已确认而仍只有兼容线路，检查教师是否完成显式绑定。定时扫描只更新候选和线路状态。

运行 `go test -p 1 ./internal/infrastructure/db/migrations ./internal/beichen ./internal/controlplane -run 'Test(BeichenUpgradeChain|AccountRateConflictBlocks|PeerKeyConflict|RebindRestoresOnly|ScheduledBeichenMaterialization|GeminiCatalogMaterialization)' -count=1 -v` 复查回归。用例覆盖 0065 未改动的凭据经 0067 回填后仍需确认、0072 原 SQL 重放、账单缺分组号仍识别倍率冲突、两把 Key 顺序采价和 401/403 后旧重绑令牌失效。`NEBULA_TEST_POSTGRES` 支持白名单内的本机 `nebula_test_integration` 与 `nebula_test_rev_integration`。已配置却不合规的目录集成测试连接会报错。

1. 在隔离的本机 PostgreSQL 测试库运行 `go test ./...`。设置 `NEBULA_TEST_POSTGRES` 指向回环地址上的 `nebula_test` 后，可验证报价快照、小数精度、重复流水、延迟结算及并发超额。前端运行 `npm run typecheck`、`npm run test` 和 `npm run build`。
2. 从已核实的学校 HTTPS 入口核对镜像版本与收件公钥。教师页应直接显示同一模型的首选倍率和启用状态。目录有模型但暂缺倍率时要显示待同步。核对价格时查看同一把北辰 Key 的分组、基础输入输出价、缓存价格、倍率和适用时段。
3. 校方准备好真实验收后，使用同一把学生 Key，四条渠道各发一笔短文本请求。依次记录学生可用额、学校请求编号、上游响应编号，以及北辰官网使用明细的数字 Key ID、模型、用量和实扣。收到响应但账单尚未出现时保留待核，继续读取相同编号。
4. 校内扣分应等于该次基础积分乘发件时的学校系数。把北辰实扣乘 100 换为积分后，再与校内扣分相减。此差额可为正或负。缺价格的调用可能靠官方流水补记，验证时也要记录基础积分来源。
5. 后台目录、价格及流水均报超时时检查学校后端的 `HTTPS_PROXY`、`NO_PROXY` 和北辰官网授权状态。若提示官网要求额外登录验证，学校的网页登录仍待授权。在个人浏览器打开北辰官网只会影响该浏览器的登录状态。核对后台读取是否恢复时，查看「供应商管理 → 北辰 → 查看模型」中的网页报价及逐笔流水成功时间。
6. 项目已经扣分但所选日期没有逐笔记录时，先核对北京日期和网页读取进度。最后成功读取一页和已读到末页分别反映最近活动和完整进度。没有成功时间时，空列表只能说明学校尚未归档该日流水。账单已有记录而学校扣分栏待核时，可在「项目监控」核对该学生的调用。使用隔离的本机测试库运行 `go test ./internal/beichen -run TestReceiptSweepPostgresG3 -count=1`，覆盖上游更正金额、同一时间分页、实扣为零和无法确认归属的记录。

### 北辰教师端用量、定价与对账

#### 教师模型报表与学生账本的笔数

1. 比较相同学生、项目、Key 和北辰范围时，先确认两边都包含同一批仍可查询的调用。教师模型报表用北京时间的派发时间选区间，学生账本和教师「学生用量」用受理账月。月末受理、次月零点派发时，分别查看教师模型报表的次月和学生账本的原账月。
2. 教师模型报表的旧 `calls` 等于 `dispatched_calls`。含待核账和失败后释放的已派发调用。对照学生账本的 `calls` 时读取教师的 `settled_calls`。例如 127 笔派发中有 5 笔上游失败后释放，已结算为 122 笔。
3. 金额为零时同时核对 `zero_cost_calls`、`token_missing_calls` 和 `pending_review_calls`。它们分别记录零额结算、没有可用 Token 计数、尚待核实的账。待核笔数大于零时，`pending_review_credits` 仍可能为零。Token 部分可用时结合 `completeness` 和 `tokens_missing` 检查已知分项。无北辰精确扣分的已结算旧行应按 `credit_milli × 0.001` 计入；400 千分积分对应 0.400。
4. 某型号用量显示为横线或 0 时，查看报表中 `model_key`、`school_model_id` 和 `upstream_models`。已保存的校内 UUID 在换绑、停用后仍指向同一归组。只有 `identity_sources=["upstream_name"]` 的历史行还缺少校内身份，核对当时供应商和渠道的明确别名，确认拼写确实指向同一个校内型号后再配置。不同版本或不同已存校内 UUID 各自核对。无 UUID、无别名时，不同渠道的同名调用应各自有包含 `credential_id` 的 `model_key`，确认前不合并。

设置隔离的本机 `NEBULA_TEST_POSTGRES` 后运行 `go test -p 1 ./internal/beichen -run 'TestModelUsage(SettlementAndLedger|Identity|FallbackSeparatesChannels|Completeness|LegacySettlement)Postgres' -count=1 -v`。用例核对 127/122、待核释放前后、零额和缺失 Token、8 个现行型号的夹具积分和 Token、点号与连字符、渠道别名优先级、换绑和停用历史、旧账 400 千分积分，以及跨渠道同名旧调用分别成行，还覆盖北京时间跨月。运行 `go test ./internal/api/http -run TestModelUsageHandlerDefaultsToAllAndReturnsEnvelope -count=1` 核对返回字段和旧字段兼容性。

#### 教师概览与对账排查

教师页若显示「目录待复核」，展开「目录来源与分组」，核对实际绑定线路的 Key、分组、目录和探活。目录超过 30 分钟、最近扫描失败或早于已知的换组轮次时，原证据不计入覆盖。重新扫当前 Key 后，报价和探活分别随当轮证据恢复。其他 Key 未列出该型号时只显示目录详情。官网清单待刷新取决于清单时效及校内已启用 Key 是否有匹配，官网仍启用的未匹配 Key 单列。

设置 `NEBULA_TEST_POSTGRES` 后执行 `go test ./internal/beichen ./internal/dataplane -run 'TestBeichenTeacherCoveragePostgres|TestBeichenOverviewRetiredCatalogPostgres|TestBeichenCapabilitiesAndGatewayPostgres' -count=1`。前端执行 `npm run test -- BeichenSchoolModels TeacherModels BeichenPriceDrawer`。回归覆盖 G3 / GM1 六个型号、绑定 Key 去重、原始上游名和校内别名、条件价、换组及目录过期、能力不可读和作废型号历史用量入口。

「北辰对账」默认总计的下一页若返回 503，核对请求是否使用同一账期、口径和供应商。旧版把查询时刻写进游标，同一天的下一次请求也会失效。当前查询把截止固定在北京时间次日 00:00，换账期或跨过零点后从第一页刷新。页面上的「重试本页」会重新请求刚才失败的那一页。

采价配置变更后，若停用渠道又导致后续账日缺同步，检查是否应用迁移 `0067`。在本机隔离测试库对照 `provider_credentials.retired_at` 和 `updated_at`，确认改采价配置只更新版本时间，停用当天继续计入覆盖要求。执行 `go test -p 1 ./internal/beichen -run 'Test(ReconciliationUnmatchedCursorPostgres|ReconciliationRetiredCredentialStopsExpectedDaysPostgres|SaveCredentialRetiredAtPostgres|ReconciliationPostgresVerifiedCallsRequireUnconflictedReceipt)$' -count=1` 可复查分页和停用行为。进入 `frontend` 执行 `npm run test -- --run BeichenReconcile`，复查账期切换、失败重试和严格视图的加载、错误及空态。

#### 官网目录模型数少于网页渠道页

打开「模型管理 → 北辰」，检查「官网目录」的最近完整读取时间，并展开具体模型的「目录来源与分组」。收到官网错误时，页面会保留已存的型号并提示待更新。官网有 Claude 但接替 Key 的目录没有时，核查该行的平台、分组和 CC1 新 Key，选择对应 Key 重扫。重扫成功后仍为零时，核对新 Key 的上游权限、分组和模型原名。出现多个近似名称时，可为正确的上游名称配置教师别名。

配置隔离的本机 `NEBULA_TEST_POSTGRES` 后运行 `go test ./internal/beichen -run 'TestBeichenWebsiteDirectoryPostgres|TestBeichenDirectoryNameMatching|TestBeichenPanelPricesMatchEquivalentKeySpelling' -count=1 -v`。测试仅用构造数据和假上游，覆盖多平台完整入库、空列表、空价格、名称变体和教师别名、仅官网型号、换 Key 后重新匹配、部分更新、失败保留和退避。运行 `npm test -- BeichenSchoolModels TeacherProviders` 检查教师页显示和指定 Key 重扫。

从仓库根目录运行 `go run ./cmd/beichen-crawl --fixture ./internal/beichen/testdata/website-directory.json`。构造样本应读到 9 个平台、20 个去重型号和 5 个分组。可给 `--fixture` 传入完整的官网响应对象或其中的渠道数组。JSON 输出中的 `complete` 为 `true` 时，`model_count` 和 `expected_model_count` 一致，且 `failures` 为空。每个型号的 `sources` 保留各平台和分组。`price_complete` 表示报价包含所声明计价方式所需的价格字段，固定图像按次报价也可完整。明示零单价按数字处理，空白、`null` 和未列出的字段在 `missing_price_fields` 中逐项显示。附带费用被忽略时，`ignored_fields` 给出字段名。

遇到 OpenAI 长上下文报价显示格式无效时，用 `go run ./cmd/beichen-crawl --fixture ./internal/beichen/testdata/website-pricing-2026-11-21.json` 离线复查。旧解析会拒绝 `tier_label` 及输入、输出、缓存读写的四个档位倍率。运行 `go test ./internal/usage ./internal/beichen -run 'Test(BeichenMultiplierTier|BeichenDirectoryFixedNonTokenQuote|BeichenCrawlRealPriceFixture|BeichenCrawlPriceHistoryAndKeyModelsPostgres)' -count=1 -v`，验证 35 个型号、499,999 和 500,000 的计价边界、分组不同价、重复抓取及价格或规则变更。PostgreSQL 用例使用 `NEBULA_TEST_POSTGRES` 指定的隔离测试库。

浏览器价格弹层从 `channels/available` 的 `supported_models[].pricing` 读取，悬停和切换型号不会请求另一份价表。价格结构中的缓存 1h/12h、图片、视频单价、分档和推理强度倍率按发布值保留。遇到 Key 只列出少数型号时，查看「Key 实际型号核对」中的分组 ID 和错误。只有该 Key 的 `GET /v1/models` 返回成功且匹配其当前分组时，才计入匹配。HTTP 403 或分组待核实时，确认支持目录读取的 Key 与目标分组已经匹配，再刷新官网目录。

网页通过 `GET /api/v1/groups/rates` 在分组默认倍率之外显示当前账号的倍率。该端点只返回分组 ID 和倍率，不提供来源或 Key 编号；目录分组直接命中时省略 `account_rate_source` 和 `account_rate_key_id`。核对分组报价时同时比较默认倍率、账户倍率和有效倍率。该请求整张失败、缺少当前分组或只有其他分组单条损坏时，会继续检查当前分组下每把 Key 的账单证据；无冲突且标准余额计费、默认倍率有效、高峰明确关闭时可以回退，有冲突时保持待核并保留冲突原因。`website_directory.last_run.account_rate_error` 在无冲突整表失败时记录「官网账户倍率表读取失败」，有冲突时记录「北辰账户倍率证据冲突」及具体原因。回归用例分别改变默认倍率、价格和个人倍率，并验证失败前后不会产生虚假的历史版本。

CC1 的账单提示「北辰官网账户倍率未核实」时，先区分无证据和证据冲突。`官网账户倍率表读取失败` 表示整张表失败，但仍要继续检查当前 Key 账单是否给出与分组默认不同的明确倍率；没有冲突时可按分组默认值回退，有冲突时整组待核。响应成功但缺少当前分组、账单 404 或账单没有有效倍率同样属于无账户证据，不代表可以忽略其他 Key 已给出的冲突。同一分组多把 Key 冲突时，`account_rate_error` 必须保留「北辰账户倍率证据冲突」原因。

Key 账单回退要求同一把活跃 Key、同一分组、账单分组号和 Key 编号明确、账单默认倍率与官网一致、高峰状态明确关闭、账单样本仍有效，并与最近 30 分钟抓取的北京时间当天同 Key 同组完整用量精确匹配。完整用量需有请求，实付除以原价的比值必须等于账单生效倍率。共用官网目录只有该组仅有一把活跃 Key 时才能发布 `key_billing`。`Key 账单读取失败（HTTP 404）` 表示接口未找到；`Key 账单缺少分组号（group_id 未提供或为 0）` 表示标准账单未提供回退所需的独立分组证据。分组号、Key 编号、默认倍率、高峰状态或采样时间不符时，页面指出对应校验项。用量不足时可在教师页点「核对这个 Key」刷新官网用量后再同步。这些 Key 账单错误只有在 `group_rate` 也无法形成时才可能进入 `billing.last_error`；`group_rate` 成功时，缺少 Key 账单证据也可以完成同步。Key 账单或型号清单返回 `GROUP_NOT_ALLOWED` 时，分组状态为 `not_allowed`，当前报价失效。

Key 证据不足时，只有标准余额计费、分组默认倍率有效且高峰明确关闭的分组才发布 `group_rate`。`account_rate_multiplier` 取默认倍率，`effective_multiplier` 保持同一值，`group_rate` 不带个人 Key 编号。高峰开启或状态未知时不得回退，新报价待核。整表读取失败不会自动阻止所有分组：无冲突分组仍可判断回退，冲突优先阻断。同一分组只要任一 Key 出现账单倍率与默认不同、账单分组号冲突或高峰冲突，其他 Key 的 Key 账单或分组默认值都不能覆盖该冲突。目录摘要出现「部分北辰渠道的账户倍率未核实」时，说明至少一个已检查渠道既没有 Key 账单证据，也不满足 `group_rate` 条件；出现「北辰账户倍率证据冲突」时，按冲突 Key 的账单倍率、分组号和高峰状态排查。

在隔离的本机 PostgreSQL 上，运行 `go test ./internal/beichen ./internal/infrastructure/db -run 'TestWebsiteKeyBillingAccountRates|TestCrawlAccountRateGroupConflictPriority|TestKeyCapabilityAccountRateEvidence|TestBeichenAccountRateKeyIdentityPostgres|TestBeichenDirectAccountRateFreshnessPostgres|TestBeichenPersonalQuoteAfterGroupMovePostgres|TestBeichenPersonalQuoteAfterGroupRemovedPostgres|TestBeichenAgedDirectKeySnapshotPostgres|TestBeichenCC1AccountRateFallbackPostgres|TestBeichenBillingGroupRefreshPostgres|TestBeichenMigration0070AccountRateSource|TestBeichenMigration0071GroupRateSource' -count=1`。覆盖交付会话、官网账户倍率优先、目录直接值不产生 Key 编号、Key 能力旧证据清理、同 Key 用量刷新不丢倍率、Key 编号变化或同 Key 换组/无组后旧个人倍率和报价失效、证据修订不匹配只隐藏账户倍率、当前官网直接表值覆盖旧 Key 快照、有效高峰下的直接表值、Key-only 未标注倍率不冒充官网表值、表缺组时的 Key 账单回退、单活跃 Key 限制、近 30 分钟完整用量、`group_rate` 高峰关闭、同组一冲突一可回退、同组两冲突和无冲突回退、整表失败按分组判断、官网未列组 Key-only 路径、拒绝原因入库、有效报价恢复与失效、0070 从 0069 升级，以及 0071 保留已有 `key_billing`、允许高峰关闭的 `group_rate`、拒绝高峰开启或未知高峰和未知来源。测试夹具中的 CC1 0.60、1.2 只用于验证倍率分支，不代表线上报价或资金结果。

Key 卡片显示旧个人倍率、错误 Key 编号或旧直接表值时，检查 `beichen_datasets.website_group_observation` 的 `billing_account_rate`。首次成功计费前，Key 卡片可显示该 Key 30 分钟内且分组配对的官网直读值；过期后清除。`credential_revision` 和 `last_success` 必须与当前 billing 数据集一致；`key_id` 必须对应当前 `website_key_id` 和官网清单中的 Key。仅证据轮次或修订不匹配时，只隐藏账户倍率，不单独撤销报价；确认 Key 编号变化、同 Key 换组或无组时，失效旧倍率和报价，历史版本仍保留。Key 清单省略分组时，一旦匹配到新 Key 编号，旧报价就会退出当前计价。在途任务提交时校验凭据修订、租约和按请求时间排序的 `confirmed_key_binding`。独立回归 `go test ./internal/beichen ./internal/infrastructure/db -run '^TestIndependentReview' -count=1 -v` 覆盖旧请求延迟返回、用量刷新换组或换号、明确拒绝分组权限、高峰报价和迁移保留其他约束。旧快照失效时只采用当前 billing 成功轮次写入的官网直接表值，该值不产生 Key 编号；Key-only 倍率也不冒充官网直接表值。`group_rate` 不依赖该子对象，直接从匹配的当前倍率版本读取。

若北辰有价而教师页显示缺价，运行 `go run ./cmd/beichen-crawl --fixture ./internal/beichen/testdata/website-price-details.json`。以 `sources[].price_complete` 确认官网报价，在 `base_token_prices_per_million` 查每百万 Token 的原价。`cache_write_price` 是 5 分钟缓存写入价，`cache_write_1h_price` 是 1 小时缓存写入价，`cache_read_price` 是缓存读取价。Claude 的 1 小时价为 `null`，对应未列单价。官网分组与 Key 分组不同时，到当前模型的「目录来源与分组」核对两个分组号。运行 `go test ./internal/beichen -run TestBeichenWebsitePriceDetails -count=1 -v` 与 `npm test -- BeichenSchoolModels` 验收原价、同组匹配和教师页展示。

Group 16 的渠道目录缺少正式价时，查看模型页是否标记「反推价，待核对」及观察时间。若报价缺失，先核对 `0058` 是否已应用、Key 是否仍属于 group 16，以及实际型号清单和当前倍率是否核实成功。用隔离的 PostgreSQL 运行 `go test ./internal/beichen -run '^TestBeichenDerivedPricesPostgres$' -count=1 -v`，回归两款 Claude 的自动结算、缓存缺价待核账、手动修正，以及官网固定价和分档价生效后的版本切换。

若 Key 清单有 group 5 而渠道目录未列出该组，核对计费或型号清单的错误码。明确返回 `GROUP_NOT_ALLOWED` 时，模型行和 Key 卡片应显示「账号无 group 5 权限」，并保留分组名和倍率。Key 清单匹配成功但未绑定分组时显示「Key 分组不存在」，缺少归属证据时显示「分组待同步」。计费倍率已核实、官方价表缺失且没有适用流水证据的模型显示「官方价表缺失」。

0.23.1 的 `0059` 只添加字段，历史的「北辰面板找不到当前 Key 分组」仍可能留在 Key 核对和价格同步错误中。即使 `datasets[]` 已有 `not_allowed`，旧卡片也会直接读取独立保存的错误。排查时通过教师概览 GET 对照相同 `credential_id` 的 `datasets[]` 中 `kind=billing` 的 `group_status` 与 `key_capabilities[]` 的分组。概览响应带 `Cache-Control: private, no-store`，且数据库缺少 `0059` 的字段时概览读取会失败。字段已存在但没有本轮观测时，等待同步或核对该 Key 后可见新的分组状态。

在隔离测试库运行 `go test ./internal/beichen -run 'TestBeichen(GroupDenialError|BillingGroupDenialPostgres|WebsiteCC2KeyUsagePostgres|DerivedPricesPostgres)$' -count=1 -v`。在 `frontend` 下运行 `npm run test -- BeichenKeyCapabilities BeichenSchoolModels`，覆盖旧错误、三个异常状态、G3 的倍率和模型、CC2 的倍率核对。测试使用预设响应。

用 `go test ./cmd/beichen-crawl ./internal/beichen -run 'Test(BeichenCrawl|CrawlCommand|BeichenWebsiteDirectoryPostgres)' -count=1 -v` 复查退出码、完整计数、单平台损坏、空价与零价、教师别名和抓取记录入库。看到 `complete=false` 时，用 `failures[].path` 和 `reason` 定位错误。网页不可读时教师页上次抓取可显示 0，先前已保存的目录仍可用于查看。需要核对 Key 时加 `--match-db` 并设置 `DATABASE_URL`，`key_matching=not_checked` 表示本轮没有执行这项核对。`key_matching=failed` 时查看 `key_match_error`。

#### 北辰网页登录、续期或抓取失败

本机窗口和爬虫可运行 `go test ./internal/beichen ./cmd/beichen-session ./cmd/beichen-crawl -run 'Test(LocalLogin|LoginWindow|CrawlCommand)' -count=1 -v`。本地假服务覆盖密码成功与拒绝、验证码、过期自动续期、并发 401、加密与文件权限、窗口关闭、以及抓取失败项。学校数据库路径可在隔离 PostgreSQL 运行 `go test ./internal/beichen ./internal/api/http -run TestBeichenPanelAccount -count=1 -v`。教师登录面板用 `npm test -- BeichenLoginPanel` 核对登录后的保存提示和测试抓取摘要。

| 现象 | 检查与处理 |
|---|---|
| 页面提示网页 Token 已过期 | 在北辰官网重新登录，更新已保存的网页 Token 后点「验证」 |
| 密码模式提示需要人工验证或登录被拒 | 核对账号，在浏览器完成北辰要求的验证。有可用网页 Token 时可切换到 Token 模式 |
| 登录状态显示读写或加解密失败 | 在目标数据库运行显式迁移，核对进程使用同一数据库和凭据加密密钥 |
| 平台或模型数为 0 | 查看最近抓取时间、失败计数和最后一次验证状态，核对网页账号对北辰渠道的权限 |
| 本机登录窗口未打开 | 检查 Windows 默认浏览器，再运行 `go run ./cmd/beichen-session login --window` |
| 本机会话文件读写失败 | 在 PowerShell 运行 `icacls "$env:LOCALAPPDATA\Nebula\beichen-session.json"` 和 `icacls "$env:LOCALAPPDATA\Nebula\beichen-session.key"`，核对当前账号是唯一的授权主体。会话文件损坏时可重新登录覆盖 |
| 本机提示重新登录 | 打开本机登录窗口，重新登录后点「测试抓取」。北辰未发续期凭证时，Token 到期需再次登录 |
| 窗口显示部分抓取失败 | 按失败项中的平台、字段位置和原因核对北辰渠道页。窗口仍会显示本轮可读的平台及去重模型数 |
| 清除网页登录后所有渠道报价都提示「请先配置北辰网页账号或 Token」 | 清除登录会保留一条空记录，用来阻止旧的环境令牌或旧会话复活；随版本交付的学校网页会话仍应继续读取官网价。先核对教师接口 `GET /api/v1/teacher/providers/{provider_id}/beichen/account` 的 `web_session_configured=true` 和 `session_release_sha256`，确认已加载本轮交付文件，再刷新官网 Key 与价格同步。仍失败时查看同一把 Key 的错误和「目录来源与分组」 |

设置隔离的本机 `NEBULA_TEST_POSTGRES` 后运行 `go test ./internal/beichen -run '^Test(ReviewCleared|ClearedPanelAccount)' -count=1 -v`。用模拟学校会话核对清除后的定时目录、Key 清单、渠道发现、逐 Key 采价、账单归属和旧凭据拦截。

#### 核对北辰官网 Key 清单

设置隔离的本机 `NEBULA_TEST_POSTGRES` 后运行 `go test ./internal/beichen -run TestBeichenWebsiteKeysPostgres -count=1 -v`，前端运行 `npm test -- BeichenWebsiteKeys`。构造样例覆盖「cc不满血」从旧倍率 0.15 更新到 `[CC2]ClaudeCode高智` 的 0.6、哈吉米 0.3、国模 GM1、分组信息缺失、已停用 Key、重复抓取、旧采价任务作废和 HTTP 401。测试服务器仅接收已登录网页所需的 GET 请求。

进入「供应商管理 → 北辰官网 Key 清单」，点「刷新官网 Key」并核对最近成功读取时间。出现「匹配不唯一」时复核官网的 Key 前缀和学校渠道的对应关系。出现「待核实」时查看北辰密钥页的分组及渠道目录的倍率字段。会话过期时在「北辰网页登录」恢复授权，再刷新清单。

运行 `go run ./cmd/beichen-crawl --keys`，核对 `keys[].group_id`、`group`、`rate`、`peak` 与北辰密钥页，同时查看 `group_check` 和同组 `models`。`missing_group` 表示目录未列出分组，`no_models` 表示目录列出分组但没有型号，两者显示「该分组未分配模型」。目录的分组或型号无法完整解析时，空列表显示 `unverified`。只有报价无法解析时，仍可判断模型归属，并保留报价失败及原有非零退出码。

同一次 `--keys` 输出还包含 `actual_models`、`actual_model_count` 与对照字段 `matched`、`only_key`、`only_group`。先看 `model_check`，只有 `empty` 确认这把 Key 的接口返回 0 个型号。`unverified` 时检查官网是否只返回遮盖后的 Key，`unavailable` 时查看 `model_message` 中的 HTTP 状态或格式错误。官网分组缺失时仍会请求该把 Key 的模型清单。学校凭据已配置时，可以用 `--compare-models` 查看学校渠道的独立对照结果。

用 `go test ./cmd/beichen-crawl ./internal/beichen -run 'Test(CrawlCommand|BeichenCrawlWebsiteKey)' -count=1 -v` 验证分页、脱敏、同组去重、部分解析及显式保存。配置隔离的 `NEBULA_TEST_POSTGRES` 后，该命令也覆盖定时到期刷新、基础倍率和高峰倍率分别变更、重复读数去重，以及分页失败保留上次数据。`changed_at` 为空时，检查教师清单的 `observed_at` 是否已有首个版本。`last_seen` 长时间未更新时，检查同步状态、北辰网页登录授权和数据库迁移 `0056`。

#### 核对北辰扫描完整性

设置隔离的本机 `NEBULA_TEST_POSTGRES` 后运行 `go test ./internal/beichen ./internal/catalog ./internal/controlplane -run TestBeichenScanRobustness -count=1 -v`。模拟上游覆盖游标和 Link、分页总数变化、第 2 页失败、HTTP 429、超时、旧轮次晚写、官网省略平台、遮罩证据不足及 Key 身份冲突。`go test ./cmd/server -run TestBeichenProvisionPostgres -count=1 -v` 核对已归档凭据的部署升级。

同一命令还覆盖官网平台交错缺席七轮、坏报价下缺席四轮、101 条 Key 等量增删后的分页漂移，以及旧会话目录响应晚于新会话 Key 清单入库。

设置隔离的本机 `NEBULA_TEST_POSTGRES` 后，运行 `go test ./internal/beichen -run 'Test(WebsiteSnapshotSessionVersionPostgres|CapturedWebSession)' -count=1 -v`。测试暂停目录和 Key 清单的旧请求，覆盖版本交付会话替换、清除教师网页登录后的交付会话续期、数据库网页登录替换。它还核对旧结果被丢弃、新会话可再次保存、刷新失败、轮换文件写入重试和并发 401 共用一次续期。

三路部署配置使用 `scripts/seal-beichen.ps1 -SkipGemini` 加密。如果提示缺少渠道，核对 CC1、GM1 和 G3 是否都已填入且 Key 各自唯一。运行 `go test ./internal/infrastructure/schoolseal ./cmd/seal-beichen -count=1` 核对三路密文往返、旧四路密文读取及证书校验。

- 若 Key 的型号总数比上一份目录减少至少一半，检查 `catalog.last_error` 中的确认进度。第三次连续返回同一完整型号集合时接受这次变化。不同集合重新从第一次计数，读取失败会清除计数并保留上次目录。HTTP 429 和单次请求超时按剩余时限最多重试一次。超过 2 秒的 `Retry-After` 留待下一轮处理。
- 若官网目录缺少平台或分组，检查 `website_directory.last_run.failures` 及 `website_directory.groups` 中的采集时间和 `retained`。缺席或缩水的平台各需要三个连续一致的完整型号及分组观察。报价错误仍记录在本轮失败项中，清单完整时可以累计确认。平台确认下线后从快照移除，其他待核验平台继续沿用。整页没有可读型号时保留快照并记录读取失败。`beichen-crawl --match-db` 可查询对照结果，确认轮数由持久化抓取推进。
- 如果官网 Key 清单未更新，先用 `last_seen` 找最近一次完整成功，再查 `failed_page` 与 `last_error`。多页清单完整复读一次，两次结果一致才更新。如果翻页时一删一增导致结果不同，页面保留旧清单并提示待核验，等上游清单稳定后可再点「刷新官网 Key」。`match_issues` 用于区分遮罩或归属证据不足、同强度候选、多个校内渠道认领同一官网 Key。
- 如果目录或 Key 清单提示「北辰网页会话已更换」，说明本轮完成读取时使用的会话已经被续期或替换。目录可核对 `website_directory.last_run.failures` 的 `path=session` 与 `last_success`。Key 清单显示 `unavailable`，可核对 `last_error` 和 `last_seen`。两处都保留上次成功数据，当前扫描结束后可分别点「测试抓取」和「刷新官网 Key」。
- 如果日用量只更新了部分日期，检查 `kind=key_usage` 的 `last_error` 和 Key 用量错误。错误附带失败账日、页码及该日旧快照时间。单日 100 页和 16 MiB 的读取上限、途中失败或总数变化都不会提交当日半份数据。账单缺字段时查 `kind=billing` 的 `website_group_observation.key_billing_missing_fields`，有效价表仍以 `kind=prices` 的状态为准。

#### 改分组后仍显示旧倍率或缺报价

旧模型页的定向重扫只读模型清单，旧概览的分组取自上次采价或预配映射。新版在「用指定 Key 重扫」后读取当前 Key 账单。查看「Key 分组与倍率」中的生效倍率、分组与上次成功时间。若显示「倍率已陈旧」，查看该行的账单错误并重扫。第三次连续失败会同时写入服务告警。待核的跟随价调用可由通过校验的北辰回执补记。

行内出现「分组报价需核对」时，对照「官网分组 X 价」与「Key 分组 Y 价」及各自倍率。显示「Key 分组不含该模型」时，可在北辰为该 Key 选对应分组，或从供应商管理换用对应 Key，再重扫该 Key。

设置隔离的本机 `NEBULA_TEST_POSTGRES` 后运行 `go test ./internal/beichen -run TestBeichenBillingGroupRefreshPostgres -count=1 -v`。构造数据覆盖 0.15 到 0.6 的切换、同平台不同分组单价、迟到的旧调用、仅高峰规则变更、官网读取失败、过期账单响应和连续失败告警。教师页运行 `npm test -- BeichenSchoolModels` 核对倍率与分组差异。

#### 重建预览过期或绑定 Key 无分组权限

- 换组后确认旧预览应返回 409，并提示重新预览。对照教师概览的 `bound_catalog_pending`、`ready_count` 与新预览的 `evidence_versions`。同步持有事务锁时确认同样返回 409。测试使用数据库触发器，在确认已读取分组证据、即将插入 Responses 绑定时暂停，由另一连接提交从分组 10 到 12 的变化。恢复确认后应返回冲突，数据库中的 Responses 绑定数量应为 0。
- 重扫明确返回 `GROUP_NOT_ALLOWED` 时，核对 `bound_routes[].group_status=not_allowed`、`binding_issue.code=group_not_allowed` 和教师行的恢复权限提示。恢复该 Key 的权限后重扫。成功扫描会清除目录拒绝，新读到的型号立即入库。旧型号在三轮完整扫描中都缺席才失去目录证据。若仅 GM1 备用线路换组，仍就绪的 G3 继续显示可用及报价。两把 Key 都已绑定时，预期 Key 覆盖为 `1/2`。
- 在隔离测试库运行 `go test ./internal/beichen ./internal/dataplane -run 'Test(RebindRejectsGroupVersionChange|KeyCatalogGroupDenialRecovery|CatalogQualityAliasCRUDAndRebind|BeichenTeacherCoverage|StudentKeyModelAuthorization)Postgres' -count=1`，并在 `frontend` 运行 `npm test -- BeichenSchoolModels`。日志回归检查成功事件的操作人、供应商和变更数量，以及预览令牌和变更明细是否已移除。

#### 核对一把 Key 的实际用量和分组

进入「模型管理 → 北辰 → Key 实际能力」，按渠道名找到对应 Key，点击「核对这个 Key」。可在弹层填写目标分组倍率，再点「开始核对」。官网分组名和倍率来自登录后的 Key 清单与可用渠道。切组当日的账单实付比值可包含切换前用量。用量显示陈旧时，查看具体账日的抓取错误。官网拒绝授权时先在「北辰网页登录」验证会话。若返回的流水属于另一把 Key 或分页缺失，保留上次核实数据，检查网页的 Key ID 和日期筛选后重试。

使用隔离的本机 PostgreSQL，运行 `go test ./internal/beichen -run 'TestWebsiteCC2GroupCatalog|TestBeichenWebsiteCC2KeyUsagePostgres|TestWebsiteUsageMoneyAndEmptyFields' -count=1 -v`。构造数据覆盖四把 Key、0.3× 与 0.6× 两个 Claude 分组、GM1、同名模型跨组异价、USD 实付、显式零、缺失和可选图像及视频字段。测试还核对两份原始 JSON、账号级 0.16× 旧数据隔离、错误 Key 流水拒绝与陈旧标记。CC2 fixture 的型号和单价用于验收，需要使用获准的真实网页会话确认官网清单与价格。前端运行 `npm test -- BeichenKeyCapabilities BeichenSchoolModels BeichenReconcile` 核对页面。

#### 探活与目录差异

设置 `NEBULA_TEST_POSTGRES` 为隔离的本机 `nebula_test` 后运行 `go test ./internal/beichen -run 'TestBeichenProbeAndCatalogPostgres|TestSingleAndBatchEnableUseSamePricesPostgres' -count=1`。探活测试使用模拟 HTTP 响应，核对协议请求的输出限制、重复排队、每小时触发、结果脱敏和 Key 轮换后的隔离。前端运行 `npm test -- BeichenSchoolModels TeacherProviders` 验证健康项、探活状态和目录差异。

- 「待探活」时先核对该 Key 是否启用、目录是否已同步，以及模型是否登记了可限制为 1 个输出 Token 的生成协议。检查 `BEICHEN_PROBES_ENABLED` 后可点「立即探活」，再用教师探活 GET 查看状态、错误代码及 HTTP 状态。北辰若拒绝 1 Token 限制，失败结果会保留，方便核实当前协议及其参数要求。
- 「已启用 · 线路异常」时查看模型下的说明，并在供应商页核对 Key 状态和目录差异。目录差异显示「待核对」时，先核实网页读取授权和渠道分组，再同步。纯 Token 条件价需有有效的输入、输出费率及可判定的档位，才能计入报价覆盖。

用 `go test ./internal/usage -run 'TestBeichenTokenQuoteBlocker|TestEvaluateBeichenConditional' -count=1` 验证空计价模式、非 Token 报价、分档缺价及结算。为本机测试库设置 `NEBULA_TEST_POSTGRES` 后，上面的启用测试还会核对概览、单个启用和批量启用的报价提示是否一致，以及线路不可用时是否优先显示线路原因。

#### 教师目录的校内模型身份

新 Key 已匹配且报价和探活正常，启用仍返回 `route_not_ready` 时，核对教师概览的 `school_model_id`、`preferred_routes`、`bound_routes` 与 `blockers`。同名的 `inactive` 历史目标可能使旧版误判为多目标。修复后，缺少校内关联或仍指向历史目标的候选可接入唯一已确认的当前 UUID。预览应显示同一把 Key 的可用协议，启用后在 `bound_routes` 核对每条协议和原 UUID。没有现有绑定时，可直接使用启用操作，重建绑定预览主要核对已有线路。

概览显示 `model_catalog.source=both`、`catalog_keys.state=listed` 且已有官网目录，但启用仍返回 `no_candidate` 时，查询该行的 `provider_model_catalog.catalog_source`。旧读取只接受 `key`，若生产行为 `both`，整条候选记录会从启用预览消失。修复后 `key|both` 统一作为 Key 载体，`both` 仍必须通过 Key 目录新鲜度、分组和探活检查；`website` 仍要求已有活动绑定。目录、定价、线路能力、探活和发布协议使用同一读取口径。

`catalog_source` 已确认为 `both` 且 Key 目录、价格和探活都正常，启用仍为 `no_candidate` 时，检查 `upstream_model_aliases.school_model` 是否指向不存在的 `models.model_id`。旧读取只按 `COALESCE(alias.school_model, upstream_model)` 解析，别名失效会同时丢掉别名和上游名两个有效路径。修复后别名目标存在时优先，目标不存在时回退上游名；别名与上游名指向两个不同的活跃或待配置模型时返回 `multi_school`。`no_candidate` 的 `error.details` 或批量 `skipped[].details` 提供 `rows`、`available`、`catalog_current`、`alias_missing`、`target_missing`、`key_listed`、`group_allowed` 整数计数。

设置独立的本机 `NEBULA_TEST_POSTGRES` 后运行 `go test ./internal/beichen -run '^TestEnableModel(FirstConnect.*|RetiredSameName.*|MultipleLiveTargets|PrioritizesAuthFailure)Postgres$' -count=1 -v`。用例覆盖一把 Gemini Key 的两个协议、确认报价、成功探活、未关联或旧目标关联、按名称和 UUID 启用、重复启用，以及多目标、多候选、跨 Key 拼协议、缺价和缺少校内倍率时的阻断。查看单个响应的 `error.details.code`、批量的 `skipped[].code` 或教师概览的 `blockers` 定位具体原因。多个活跃校内目标拆成两行时，两行的阻断码和提示均与单模型、批量一致，均为 `multi_school`。

运行 `go test ./internal/beichen -run '^(TestEnableModelCatalogSourceBoth|TestActivationCatalogSourceNeedsKeyEvidence|TestActivationCatalogSourceWebsiteOnlyNeedsBinding)Postgres$' -count=1 -v` 验证 `both`。生产形状使用组 `31`、倍率 `0.5`、确认价格、两种协议探活成功和唯一当前校内目标；同时覆盖 Key 目录过期、分组拒绝与 `website` 无绑定。

运行 `go test ./internal/beichen -run '^(TestEnableModel(MissingAliasFallsBackToUpstream|AliasAndUpstreamDifferentLiveTargets|ValidAliasTakesPriority)|TestActivationNoCandidateDiagnostics)Postgres$' -count=1 -v` 验证别名路径和诊断计数。用例覆盖失效别名回退、有效别名优先、两个不同活跃目标阻断，以及单模型和批量诊断字段。

测试中刚写入的 Key 目录被判为陈旧时，核对目录观测时间是否早于凭据版本或分组变更。构造当前目录的测试数据使用 PostgreSQL 时钟，避免 Windows 上应用与数据库的时钟精度差异影响时间先后。

启用成功替换了一条 active 旧线路的 Key 时，先查原 Key 的 `beichen_catalog_auth` 和绑定协议探活。当前凭据版本被 401/403 拒绝属于认证失败，不能由首接候选自动修复。单个启用应返回提示显式重绑的 400，批量应返回 `route_not_ready`，模型、绑定和审计均不应变化。停用或退役 Key 可按唯一候选修复。只有一个已停用校内目标时，教师显式启用仍可修复其已有绑定。运行 `go test ./internal/beichen -run '^TestEnableModel(DoesNotAutoRepairAuthFailedActiveRoute|DoesNotPublishCredentialCatalogAuthFailure|RepairsInactiveOrRetiredAuthFailedRoute|KeepsHealthyExistingRoute)Postgres$' -count=1 -v` 核对 401、403、目录认证标记、停用、退役和健康旧线路。运行 `go test ./internal/dataplane -run '^TestBeichenTeacherEnablesDiscoveredModelForStudent$' -count=1` 核对改名和轮换 Key 后仍沿用原 UUID。

教师概览的同上游型号必须按校内模型分别显示。设置隔离的本机 `NEBULA_TEST_POSTGRES` 后运行 `go test -p 1 ./internal/beichen -run TestBeichenTeacherCoveragePostgres -count=1 -v`。用例构造 8 个目录行，断言每行都有非空且唯一的 `school_model_id`；其中同一上游名绑定两个校内模型时返回两行，两个 UUID 不同，专属 `preferred_routes` 不混入另一校内模型。两行都必须得到官网和 Key 目录证据，状态均为 `key_listed` 而不是 `published_unavailable`；各自只保留 CC1 或 G3 的 `catalog_keys` 和 `credential_ids`，不再出现两行共享全部 Key 的宽范围结果。其余 6 个现行型号的计数、价格、Key 覆盖和分组继续由同一用例核对。

在 `frontend` 下运行 `npm test -- BeichenSchoolModels BeichenModelUsageDrawer`。现有用例核对目录按 `school_model_id` 精确取用同名上游的校内费用，也核对旧响应缺少该字段时使用校内名称和唯一线路回退，无法唯一确认时显示待确认。字段是新增的可空属性，旧接口响应仍可消费。

纯文本模型探活通过后仍显示「官网报价格式无效」或「非 Token 计价」时，先看 `price_blocker.message` 的字段名。旧解析会拒绝空字符串，并把非 Token 字段的零值当作有费用。运行 `go test ./internal/beichen -run 'TestPanelMixedPriceFixture|TestPanelMixedPricesActivationAndSettlementPostgres' -count=1 -v`，用构造的面板 JSON 和隔离的本机数据库复查。测试覆盖 gpt-6.1-sol 的固定价、gpt-6.2-sol 的两个 Token 档位、缺价档的提示与待核账、两种启用入口及扣分明细中被忽略的字段。

#### 作废渠道的历史型号

旧版会把某些仅关联停用渠道的型号继续列为线路异常。教师可刷新「模型管理 → 北辰」，在「全部模型」的「全部」筛选中查找「已作废」并打开原有用量明细，再切换「常用模型」「异常」和「待配置」确认这些型号已退出对应列表。若刷新后仍属异常，可核对教师概览的 `model_catalog[].retired`、渠道状态和绑定状态。普通换 Key 后可能仍有指向旧 Key 的启用绑定，此时「异常」应显示维护提示，可用渠道切换向导核对接替 Key。官网 Key 清单里的「未匹配校内 Key（官网仍启用）」对应 `match_status=unmatched` 和 `status=active`。

在 frontend 下运行 `npm test -- BeichenSchoolModels BeichenWebsiteKeys BeichenPriceDrawer TeacherProviders`，核对启用 Key 计数、作废行显示范围和只读操作、刷新后旧定价与批量操作的拦截、真实异常、官网清单和缺价概况。设置隔离的本机 `NEBULA_TEST_POSTGRES` 后运行 `go test ./internal/beichen -run 'TestBeichenOverviewRetiredCatalogPostgres|TestCredentialSwitchLifecyclePostgres' -count=1`，核对历史目录和绑定、同时存在启用和停用绑定的型号、仍需迁移的启用绑定、陈旧目录，以及同一型号重新列入启用 Key 目录时的概览。

后端使用隔离的本机 PostgreSQL。新建的测试库需在 `public` 安装 `citext`、`pg_trgm` 和 `pgcrypto`。全量测试的网关用例可使用下列本机端口。

验证 0.23.0 的迁移和北辰回归时，在同一测试库运行下列命令。迁移测试从 0051 升级并重复执行，检查分组报价回填和旧账保留。对账测试核实历史归档、人工更正、无记录的零用量日和缺账金额。报价测试用模拟网页触发 401，检查各渠道共享一次续期。渠道切换测试确认网页用量同步后会生成线路迁移预览。

```powershell
go test ./internal/infrastructure/db -run TestBeichenMigrationFrom0051PreservesPriceAndUsage -count=1
go test ./internal/beichen -run 'TestReconciliationUsesVerifiedArchiveWithoutRecentSnapshot|TestBeichenBillingDiagnosticsPostgres|TestScheduledWebsitePricesPostgres|TestCredentialSwitchLifecyclePostgres' -count=1
```

若历史账日显示缺账，在测试库核对归档检查点的账号、Key ID 和 `last_success`。完整归档要求读到该北京时间账日结束后，且分页已读完。若旧数据来自公开账号级用量，仍可查询历史行，教师对账从已核对的官网流水或人工导入读取。

```powershell
$env:NEBULA_TEST_POSTGRES = 'postgres://nebula_test@127.0.0.1:55841/nebula_test?sslmode=disable'
go test ./internal/usage -run TestBeichenLedgerPostgres -count=1 -v
go test ./internal/beichen -run 'TestEnableModelsBatch|TestModelUsagePostgresRangeAndAttribution|TestReportsPostgresModelsStudentsKeysAndReconciliation' -count=1 -v
go test ./internal/api/http -run 'TestRegistersControlPlaneAndGatewayRoutes|TestBeichen' -count=1 -v
go test ./...
```

第一条覆盖跟随官网、手动价、回执补记和调价不追溯；第二条覆盖批量启用、北京时间区间边界、成员与 Key 聚合以及本地/北辰 FULL JOIN；最后一条覆盖全部路由和鉴权。前端运行 `npm run typecheck`、`npm test` 和 `npm run build`，其中 `BeichenSchoolModels.test.ts`、`BeichenPriceDrawer.test.ts`、`BeichenReconcile.test.ts`、`TeacherProjectQuotaDialog.test.ts` 和 `schoolBillingFlow.test.ts` 直接覆盖本次页面。

人工验收按以下顺序：

1. 模型页的官网标准价和学生实际价都以积分显示，北辰原价在弱化小字中；四个字段（输入、输出、缓存读取、缓存写入）对齐，不再出现整段长句。切换当天/本月/总计后，每个模型的积分和 Token 与数据库一致，用量抽屉总量等于各模型之和。
2. 一键启用先显示“将启用/跳过”数量和原因。用模拟目录和报价核对两种情况。各档都有输入、输出单价的纯 Token 报价可以启用。某档缺价时，启用按钮旁和「查看档位」都应指出具体档位和缺少的单价。
3. 手动价保存后，四个 Key 的学生基础价一致；新发起的调用写入 `beichen_base_source='teacher_manual_price'`，学生实扣等于基础价乘 1.10；恢复跟随官网后，后续调用回到官网报价。保存前已经发出的调用不应改变。
4. 学生额度弹窗在 1440、1024、768、375 宽度都没有横向滚动；学生额度不能低于“已用 + 待核”，超过项目可分配额度时显示服务器错误；导师和历史月份只读；切换弹窗月份不触发项目页重新加载。
5. 北辰对账的学生已扣按“积分 ÷ 100”显示，北辰实扣按 `billed_upstream_micros ÷ 1,000,000` 显示；差额为前者减后者。北辰尚未回传实扣的模型显示“—”，不能显示 0。仅本地或仅北辰存在的模型必须出现在明细中并高亮。

常见问题：

- 手动价看起来没有生效：先用 `SELECT beichen_price_at(credential_id, model, sent_at) FROM gateway_calls WHERE id=...` 核对发件时刻。价格版本在 `effective_at` 之后才生效，调价不会追溯更早的调用。
- 探活已通过但模型因报价无法启用时，查看启用按钮旁的原因或「查看档位」。如果某档缺价，核对该档的输入、输出单价后重新同步。按次、图像、视频等报价需使用可用的手动 Token 价，才可按当前 Token 计价方式启用。
- 对账差额异常大：先看北辰实扣是否为未知、当天账单是否已同步、模型是否只有本地或只有北辰记录。缺失实扣时页面显示“—”，不会把未知当作零；差额仅供参考。
- 学生额度保存返回 `PROJECT_QUOTA_ALLOCATION_EXCEEDED`：该学生增加后的总额超过项目月额度。先减少其他成员额度或提高项目额度，不能靠前端绕过。
- 手动价出现 100 倍偏差：检查是否把“积分/百万 Token”又乘了一次 1,000,000。HTTP 新接口接收十进制积分字符串，服务端统一缩放；不要在浏览器里预先缩放。

### 北辰仪表盘与本地账本报表

执行 `go test ./cmd/beichen-report ./internal/beichen -run TestDashboard -count=1 -v`，验证日期与粒度、Key 过滤、翻页、空数据、401、金额差额和报告保存。设置 `NEBULA_TEST_POSTGRES` 为隔离的本机测试库，可同时验证按北京时间日期、模型和 Key 的对账，以及无回执、待核账和一侧缺记录。全量检查执行 `go build ./...` 和 `go test ./...`。

待核账审计报告出现 `unknown` 时，先看 `missing` 和 `note`，再核对协议的计数口径、原始输入输出总量、缓存计数，以及可产生费用的分项在派发时刻的报价。报告中的金额零值需结合分类阅读。运行 `go test ./internal/usage/pendingaudit -count=1 -v`，在无数据库连接的情况下复查协议拆分、缺项、金额区间、推荐比例和自动结算阈值。

教师批量结算返回 409 pending_audit_changed 时，重新点击「只读回放」核对最新预计扣分，再在弹窗确认。批次外新增待核（含无派发价格的历史行）或跨页使用令牌均会触发冲突。若本批无可结算项但仍有下一批，点击「查看下一批」。核对剩余数量时按供应商及 pending_review 计数，价格时间为空的记录也应计入。设置隔离本机测试库 NEBULA_TEST_POSTGRES 后运行 `go test -p 1 ./internal/dataplane -run TestBeichenPendingBacklog -count=1 -v`，核对只读事务、分页、未定价行、宽区间 1 → 28、重复确认和学生预留封顶、并发确认、单笔回滚和月度实扣。若宽区间仍保留旧值，检查批量确认是否因该行不能自动扣款而跳过了预留写入，并核对同学生其他待核行占用的额度。运行 `go test ./internal/api/http -run TestPendingAudit` 验证权限及缓存头，在 frontend 下运行 `npm test -- src/views/teacher/BeichenReconcile.test.ts` 验证弹窗、翻页及冲突提示。

- 网页提示会话过期时，先用本机登录窗口更新会话，再运行 `go run ./cmd/beichen-report --range 7d`。
- 平台卡片的请求差额在 `platforms.account_minus_platforms`，四把 Key 的差额在 `key_reconciliation.account_minus_sum`。实际金额和 Token 的差额另有字段。
- `--reconcile` 用 `DATABASE_URL` 连接本机数据库。如果北辰供应商无法唯一确认，加 `--provider <供应商UUID>`。对账行里的 `null` 表示证据缺项，具体查看 `missing_reasons`。[对账字段说明](reports/beichen-crawl-stability.md)

### 北辰 Token 缺项与逐笔差额

旧版网关会把含部分计数的调用标为 `missing`，旧报表会跳过其中保存的分项。调用数和已扣积分来自账务记录，因而可能同时出现。使用教师调用对账接口，按同一账日核对每行的原始计数、`usage_state` 与 `missing_token_reasons`。Chat 流式用量取上游使用量事件。Responses 流式和 WebSocket 使用对应响应的终止用量。Anthropic 合并开始事件和后续累计计数。Gemini 原生生成回报没有独立的缓存写入分项。

逐笔核对时用 `base_difference_credits` 比较基础积分与回执实扣，用 `difference_credits` 单独核对独立计价与实扣。学校系数与额度封顶可以另用 `school_multiplier`、`student_credits`、`school_absorbed_credits` 复核。存在差额时结合本地和回执 Token、价格档位、渠道倍率版本、推理强度和缓存费率核对。日账缺失时查看逐笔证据，区间利润保持未知。

在上述本机测试库运行 `go test ./internal/dataplane -run 'TestBeichenUsageProtocolMatrix|TestBeichenPostgresGatewayReconciliation' -count=1 -v`。假上游的 41 笔调用覆盖完整用量、缺缓存明细及缺用量，且检验逐笔回执补记、回执重放和差额精度。完整检查使用 `go build ./...` 与 `go test ./...`。

### 学生月度用量与待核

在隔离的本机 PostgreSQL 设置 `NEBULA_TEST_POSTGRES` 后运行 `go test ./internal/usage -run 'TestStudentUsageContractPostgres|TestStudentCallReceipt|TestBeichenLedgerPostgres' -count=1 -v`。测试以真实查询和响应 JSON 验证跨项目和 Key 的精确求和、完整及部分 Token、缺失历史证据、停用 Key 的零额调用、待核补记与回执访问校验。

- Token 总量大于四项之和时，查看 `token_coverage.unclassified_tokens`。分项为 `null` 时，用同一账月的逐笔 `usage_state`、`usage_reason` 和原始协议计数核对。缓存命中率的实际样本数在 `cache_hit_coverage.sample_calls`。
- 待核与在途金额差异可用同一账月的 `pending_review_calls`、`pending_review_credits` 和 `in_flight_credits` 复核。账页间的已用和次数应比较同一账月的服务端汇总 `data.totals`、`data.models`，最近调用分页可用来查对应的逐笔状态。
- 两把 Key 都能受理请求、旧 `available` 却显示为零时，检查 `available_effective` 和 `project_member_available`。前者还受项目总余额限制，后者等于所属项目的个人余额。运行 `go test ./internal/api/http -run TestStudentUsageEffectiveBalanceHTTPPostgres -count=1 -v`，复现 Key 月桶为零、两把 Key 通过受理和项目总余额不足的场景。
- 在 `frontend` 执行 `npm test -- StudentModels StudentUsage studentUsage.test.ts`，核对切换 Key 后的模型用量、精确小数余额、零额已结算次数、在途占用和待核笔数。发现跨 Key 数字混入时，核对同一项目下所选 Key 的 `models` 与 `available_effective`。模型仅保留历史授权时，用 `TestStudentKeyModelAuthorizationPostgres` 核对渠道当前目录、扫描失败和探活状态。

### 学生模型目录搜索

运行 `go test ./internal/api/http -run 'TestStudentCatalog(Batch|Bounds)HTTPPostgres' -count=1 -v`。测试记录 PostgreSQL 实际执行的 SQL，比较 1 个匹配和 23 个匹配的第二页，检查排序、候选总数、绑定优先级、协议和探活证据。搜索被拒绝时，检查 `q` 的 128 字符上限、`page` 的 1 至 10000 范围、`page_size` 的 1 至 50 范围及 URL 编码。

状态计数异常时，核对无搜索词的第一页返回的 `directory.models` 和本人 Key 授权。目录列表与统计都以 `addable` 为准。北辰 Key 下的型号明细按调用记录中的项目、学生和 Key 归属聚合。运行 `go test ./internal/beichen -run TestModelUsageKeyBreakdownUsesRecordedOwnerAndProjectPostgres -count=1` 核对同一 Key 的历史归属。

### 教师学生用量

配置隔离的本机 `NEBULA_TEST_POSTGRES` 后执行以下检查。

```powershell
go test ./internal/usage ./internal/controlplane ./internal/api/http -run 'Test(TeacherStudentUsage|StudentUsageContract)' -count=1 -v
go vet ./internal/usage ./internal/controlplane ./internal/api/http
cd frontend
npm test -- TeacherStudentUsage router
npm run typecheck
npm run build
```

PostgreSQL 样例覆盖两位同名学生、共享两个项目、每个项目多把历史 Key 和多模型，核对精确积分、调用和已知 Token 的逐层加总，以及停用或退出项目后的历史、零额、待核、账月边界、归档归属和分页排序。HTTP 测试验证教师鉴权、禁缓存、无效参数和明细为空的状态码。前端测试覆盖查询、翻页、下钻、空态、加载失败重试及切换条件后的过期明细。

- 金额看起来对不上时，使用相同的 `month`、`project_id` 对比学生行的 `credits` 和明细的 `totals.credits`，再核对项目及 Key 的 `used`。用量查询使用受理账月，调用发生时间可能跨自然月。已扣积分与 `pending_review_credits` 各有独立口径。
- 缺 Token 或最近时间但有已结算记录时，检查 `monthly_usage_cube` 是否仍有对应维度并查看明细 `token_coverage`。原始逐笔记录到期后，账本保留积分和次数。
- 切换项目后「未确认归属 / 校方使用」仍按整个学校账月汇总。两条流水共用响应编号时，先核对回执的流水行号。回执仅确认对应行。缺少回执时，核对整个派发时间窗口是否只有一条候选。`TestTeacherStudentUsageUnattributedReceiptHTTPPostgres` 覆盖同号不同流水、唯一候选和跨账月歧义。
- 列表翻页会复用最多 5 秒的报表快照。刚补记的金额可以在明细先显示，随后出现在重新读取的列表。列表读库超过 3 秒时返回 503。使用 `TestTeacherStudentUsageSnapshotHTTPPostgres`、`TestTeacherStudentUsageTimeoutHTTPPostgres` 和 `TestTeacherStudentUsageOverCacheLimitHTTPPostgres` 核对 10 万笔调用下冷热读库次数、到期刷新、超时恢复及超出缓存预算时的精确总数。

#### 10 万笔账月的查询计划

在本机 PostgreSQL 17.12 的隔离 schema 上，以同一份数据对比基线 `21673068` 与修复 SQL。样本含 12 个账月共 21 万次调用，目标账月 10 万次调用、1500 名学生及 49829 条上游归档。下表为 `EXPLAIN (ANALYZE, BUFFERS, TIMING OFF)` 三次的执行时间中位数，查询均使用实际参数的计划。

| 场景 | 主要计划节点或实际执行情况 | 中位数 |
| --- | --- | ---: |
| 基线首页 | `Parallel Seq Scan on gateway_calls`，物化 `CTE matches` 后统计和排序 | 277.370 ms |
| 基线越界页 | 同样并行扫描及物化，仍计算总人数 | 277.058 ms |
| 修复后未命中缓存 | 同样并行扫描，直接输出 1500 条学生汇总供缓存 | 281.614 ms |
| 修复后命中缓存的后续页及越界页 | 复用汇总和全校归档统计，执行 0 条报表 SQL | 无数据库报表计划 |
| 基线归档汇总 | 归档按时间筛选，以响应声明排除已归属数据 | 36.912 ms |
| 修复后归档汇总 | 按回执流水或唯一派发候选排除，计划额外含一次 `Hash Anti Join` | 44.583 ms |

首次加载仍需汇总该账月，性能收益来自省去后续页面的两条报表查询。执行 `go test ./internal/api/http -run TestTeacherStudentUsageSnapshotHTTPPostgres -count=1 -v` 可以用独立样本复核真实 HTTP 请求的冷热查询次数和耗时。

### 北辰网页报价持续为 0/4

先从已核实的学校 HTTPS 入口读 `/.well-known/nebula-release.txt`。学校若显示 0.15.0，采价任务还依赖预配的分组行。同步可以先写入 Key 账单倍率，随后因缺少分组而跳过官网价表。北辰的标准 Key 账单格式可以省略分组 ID，网页价表本身要求登录。价格成功数统计有效的报价快照，流水成功数取决于有效的上游 Key 绑定和已成功读取的流水分页。

有学校教师登录权限时，读取 `GET /api/v1/teacher/providers/{provider_id}/beichen`，核对 `datasets` 中 `catalog`、`billing`、`prices` 的时间和错误，以及 `website_sync` 的覆盖数。有校方授权的数据库维护者可用下列只读查询核对同一渠道的采价状态。结果只含分组、状态、时间和行数。

```sql
SELECT j.channel, a.group_id AS configured_group, a.upstream_key_id IS NOT NULL AS has_receipt_key,
       j.discovery_error, b.last_success AS multiplier_seen, d.last_success AS price_seen, d.last_error,
       snap.group_id AS quote_group, snap.effective_at AS quote_seen, snap.expires_at,
       snap.credential_revision = c.updated_at AS same_metadata_revision,
       (SELECT count(*) FROM beichen_price_versions v
        WHERE v.credential_id = c.id AND v.effective_at = snap.effective_at AND v.source = 'panel') AS flat_prices,
       cp.last_success AS receipt_seen
FROM beichen_provision_channels j
JOIN provider_credentials c ON c.id = j.credential_id
LEFT JOIN beichen_panel_access a ON a.credential_id = c.id
LEFT JOIN beichen_datasets b ON b.credential_id = c.id AND b.kind = 'billing'
LEFT JOIN beichen_datasets d ON d.credential_id = c.id AND d.kind = 'prices'
LEFT JOIN LATERAL (
  SELECT group_id, effective_at, expires_at, credential_revision FROM beichen_panel_snapshots
  WHERE credential_id = c.id ORDER BY effective_at DESC LIMIT 1
) snap ON true
LEFT JOIN beichen_usage_checkpoints cp ON cp.credential_id = c.id
  AND cp.revision = a.updated_at AND cp.upstream_key_id = a.upstream_key_id
WHERE c.status = 'active'
ORDER BY j.channel;
```

续期后重启仍提示网页登录失效时，核对学校后端是否挂载原来的私有卷，且服务能写入 `LAB_BEICHEN_SEAL_DIR`。续期状态文件需要由同一卷上的学校收件私钥解密。新交付的网页登录捕获使用独立的续期状态。

设置 `NEBULA_TEST_POSTGRES` 指向隔离的本机 PostgreSQL，再运行 `go test ./internal/beichen ./internal/dataplane -run 'TestSavedWebSessionSurvivesProcessRestart|TestScheduledWebsitePricesPostgres|TestPanelPostgresPriceLifecycle|TestReceiptSweepPostgresG3|TestBeichenTeacherEnablesDiscoveredModelForStudent' -count=1 -v`。覆盖真实进程重启、三轮四路采价、固定分组校验、新模型补价、已有快照升级、1.10 扣费和教师接口状态。所有上游请求使用模拟响应。
7. 预期的维护教师看不到工作区时，核对学校维护 HTTPS、登录账号的校内 UUID 和校方提供的固定应用地址。工作区配置无效时会记录简短的启动提示，学校网站继续提供服务。若能打开工作区但流水时间仍旧，需分别核验工作区登录状态和后台数据来源。运行 `go test ./internal/api/http -run 'TestSchoolWorkspace|TestBeichenOverviewPreventsSharedCaching'` 可检查教师隔离、空配置和缓存。

| 现象 | 常见根因 | 解决方案 | 排查建议 |
| --- | --- | --- | --- |
| 服务启动时报缺少环境变量 | 未加载 `.env`，或关键 secret 仍为空 | 按 `.env.example` 配置运行环境 | 先核对变量名，不要在日志/工单粘贴变量值 |
| `go` 命令不存在 | 新终端未加载用户 PATH，或 Go 未安装 | 重新打开终端，确认 Go bin 在 PATH | 运行 `go version`；本机约定安装在 `D:\dev\caches\go` |
| Ent 生成结果与 Schema 不一致 | 修改 Schema 后未运行生成器 | 运行 Ent generate 并提交生成代码 | 运行 `go generate ./internal/infrastructure/db/ent`，再检查 Git diff |
| 数据库提示 `type citext does not exist` | PostgreSQL 未启用 `citext` extension | 对正确数据库执行迁移命令 | 先确认 DSN，再运行 `go run ./cmd/migrate` |
| 数据库连接失败 | DSN、网络、账号、TLS 参数错误 | 修正 `DATABASE_URL` 或启动 PostgreSQL | 检查 5432 端口、数据库名和 `sslmode`；不要输出密码 |
| Redis 连接失败 | Redis 未启动、URL 错误或 ACL 不允许 | 启动 Redis或修正 `REDIS_URL` | 检查 6379 端口；确认 DB index 和 TLS scheme |
| 验证码一直收不到 | SMTP 配置错误、冷却中、垃圾邮件拦截 | 修正 SMTP，等待冷却后重发 | 用 request ID 查邮件发送错误；不要索取验证码 |
| 验证码提示无效或锁定 | code 过期、用途不一致、失败次数超过上限 | 重新发送对应用途验证码，等待锁定过期 | 核对 email 规范化结果、purpose、TTL 和 Redis key TTL |
| 注册提示邮箱已存在 | `users.email` 使用 citext 唯一，大小写视为相同 | 使用登录/重置密码，不要重复注册 | 用脱敏邮箱查询用户状态；检查是否 `disabled` |
| 登录一直 401 | 密码错误、账户禁用、JWT 配置变化 | 重置密码或由老师恢复账户；重新登录 | 不区分“邮箱不存在”和“密码错误”；检查服务端 request ID |
| Doppler 中学生/导师测试账号仍登录 401 | 同邮箱用户在引入 bootstrap 前已经存在，启动初始化按幂等契约不会覆盖其密码或状态 | 使用现有密码登录，或通过正式重置密码流程同步到目标密码；不要直接改 `password_hash` | 只核对用户是否存在、角色和状态，不输出邮箱全文或密码；新邮箱应在首次部署后创建为 `active` |
| Refresh 突然全部失效 | token 被轮换后重用，session family 被撤销 | 清除 Cookie 后重新登录 | 检查是否多标签页并发 refresh；确认 Redis 未被清空 |
| 浏览器没有 refresh Cookie | 非 HTTPS 环境却设置 Secure，或跨站 Cookie 被阻止 | 本地环境关闭 Secure；生产使用 HTTPS 同源 | 检查 Set-Cookie、SameSite、Path 和浏览器存储策略 |
| SSE 连接 401 | 浏览器 EventSource 无法附加 Authorization Header | 使用 fetch streaming 并携带 Bearer token | 检查请求 Header；禁止把 token 放在 URL |
| SSE 已连接但状态不更新 | Redis stream 不可用、事件过期或客户端只监听未刷新 REST | 恢复 Redis并重新拉取权威 REST 状态 | 查看 heartbeat、Last-Event-ID 和用户 stream；事件不是权威数据 |
| 学生看得到项目但提交失败 | 项目没有 ACTIVE 负责导师 | 导师申请该项目并由老师批准 | 查看项目 `has_mentor` 与 ACTIVE project member |
| 申请提示 Key 名称冲突 | 同一学生已有同名进行中/生效 Key，citext 不区分大小写 | 更换名称，或等待原申请进入 rejected/revoked | 查询自己的 Key 列表，核对状态和大小写 |
| 添加模型返回 `MODEL_NOT_READY` | 模型暂停发布、缺报价、没有有效线路或线路探活均失败 | 根据响应原因检查模型配置、绑定和探活结果 | 通过 `details.model_id` 定位模型 |
| 首次保存模型默认倍率返回 `multiplier change reason is required` | 后端把 NULL -> 默认倍率误判为倍率变更 | 首次设置倍率不要求原因；只有已有倍率改为不同值时填写变更原因 | 用默认基础配置直接保存，应返回 200 并进入 Binding 节点 |
| 老师新增模型搜不到 Matrix 候选 | `model-catalog` 未启动、`MATRIX_APIKEY` 缺失或最近同步失败 | 检查生产 worker 状态和日志；失败保留上次快照，不清表、不把目录暴露给学生 | `docker compose -p nebula-api -f compose.production.yaml ps model-catalog`；日志不得包含 API Key |
| 一键配置拒绝测试地址 | 手写 Base URL 解析到 localhost、私网、链路本地或云元数据地址 | 使用公网 HTTP/HTTPS 上游，或先注册经审核的供应商 | 不得关闭连接阶段 DNS/IP 校验或允许重定向绕过 |
| 一键配置弹窗出现在右下角、测试后仍显示大段响应 | 子表单使用页面内 fixed panel，且把 Probe 原始响应保存为渲染状态 | 一键配置和首次询问统一使用 Portal Dialog；后端只返回标准化配置与状态摘要，成功后关闭弹窗并写回第二步 | `cd frontend; npm run test -- src/views/teacher/TeacherModels.test.ts`；页面同时打开主向导和子弹窗时，两者都应在视口中央 |
| 一键配置未带出简介、标签、能力或容量始终是默认值 | Probe 未请求供应商模型目录、未精确匹配上游模型 ID，或只解析结构化容量字段 | Probe 固定先请求 `/v1/models`，将真实 description 写入表单，并统一执行 tags/model type/endpoint types/description 映射与 description 正则、模型族、全局默认容量规则 | `go test ./internal/catalog ./internal/controlplane`；不要记录目录原始响应或供应商 Key |
| Binding 加号打开的表单仍出现在右下角 | Binding 仍使用旧 `.inline-editor` 页面面板，没有进入 Dialog Portal | 新增和编辑 Binding 统一使用 `AppDialog`，删除旧 inline editor 样式 | `cd frontend; npm run test -- src/views/teacher/TeacherModels.test.ts`；打开 Binding 后应存在主向导和居中的 Binding Dialog |
| 学生 `0x` 模型显示 0 次而老师有记录 | 学生查询遗漏 `zero_cost_count` | 学生调用数使用 `charged_count + zero_cost_count`，0 credits 行仍返回 | `go test ./internal/usage`；对照学生个人用量与老师/导师免费模型汇总 |
| 学生“选择模型广场模型”点击右上角关闭后立即重开 | 搜索框用 `focus` 自动开窗，Dialog 关闭后恢复焦点触发同一事件 | 仅在用户点击搜索框时打开；焦点恢复不得改变 Dialog 状态 | `npm run test -- src/views/student/StudentKeys.test.ts`；Playwright 点击叉号并等待 300ms，页面应无 Dialog |
| 导师看不到待审 Key | 导师不是目标项目的 ACTIVE 负责成员，或已被其他导师处理 | 完成项目申请，或刷新状态 | 查项目成员关系和 Key 当前状态；首个有效初审胜出 |
| 导师/老师重复审批得到 409 | 条件更新阻止并发重复状态迁移 | 刷新详情，不要重复提交 | 检查审计时间线和当前状态，不要直接改数据库 |
| 导师审批返回 `503 DEPENDENCY_UNAVAILABLE` | 审批事务的数据库操作失败，项目月 bucket 更新必须绑定额度、时间、项目 ID 和月份 | 查看响应的 `request_id` 对应日志，处理事务错误并重试 | 运行 `go test ./internal/usage -run TestApproveKeyBindsProjectBucketMonth` |
| 老师驳回申请返回原因缺失 | 驳回请求没有填写非空审核意见；通过请求的审核意见可以省略 | 在驳回弹窗填写具体原因；通过时可以留空意见 | 期望错误码为 `REJECTION_REASON_REQUIRED`；通过请求空 body 应返回 `204` |
| 老师审批导师项目申请提示导师关系异常 | 审批时发现导师未加入项目所属组织，或导师已经是项目成员；空 body 则可能是请求体解析错误 | `MENTOR_NOT_IN_ORGANIZATION`：在“组织管理”重新分配导师；`MENTOR_ALREADY_PROJECT_MEMBER`：无需重复审批并刷新列表 | 查看响应 `error.code`；核对 `organization_members`、`project_members` 和申请状态 |
| 老师看不到 ACTIVE Key | 这是首期权限边界，不是数据丢失 | 由负责导师查看和撤销 | 老师接口只暴露 `pending_teacher` 摘要 |
| 批准申请后学生成员关系缺失 | 审批事务回滚或数据库约束失败 | 核对请求结果和事务错误，修复后重新批准 | 同时检查 Key、组织成员、项目成员和审计记录 |
| 领取接口没有再次返回完整 Key | 明文只在首次领取返回一次 | 创建新申请，不支持找回 | 只能使用 prefix 定位；任何人都不应从数据库恢复明文 |
| 领取时报 `KEY_ALREADY_CLAIMED` | Key 已是 ACTIVE 或并发请求已有一个成功 | 使用首次响应中的明文 | 检查 `claimed_at` 和 prefix，不要要求后台重显 |
| 网关返回 invalid API key | Key 不存在、HMAC pepper 变化、状态非 ACTIVE | 使用正确 Key；恢复原 pepper 或重新申请 | 只记录 prefix；检查部署间 `API_KEY_HASH_PEPPER` 是否一致 |
| 网关返回 model_not_allowed | 请求模型不在该 Key 的规范化白名单 | 使用 `GET /v1/models` 返回的模型 | 核对 `api_key_models`，不要依赖客户端缓存 |
| 北辰返回 429 `unresolved_exposure_limit` | 同一学生待核预留加本笔默认预留达到上限 | 核对跨项目和 Key 的待核金额 | 按学生 ID 执行 `SELECT count(*), coalesce(sum(unresolved_credit_milli),0) FROM gateway_calls WHERE user_id=$1 AND beichen_state='pending_review'`；同一学生非北辰路由仍可受理 |
| 网关返回 429 `student_concurrency_exceeded` | 同一学生派发中的调用已达上限，所有 Key 和项目共用名额 | 等已有调用结束后重试；直接运行或本地 Compose 可设置 `NEBULA_STUDENT_CONCURRENCY_LIMIT` 覆盖默认 5，生产 Compose 显式指定 5 | 设置本机测试库 `NEBULA_TEST_POSTGRES`，运行 `go test -p 1 ./internal/usage ./internal/dataplane -run 'TestGatewayQuotaPostgres\|TestStudentConcurrencyHTTPPostgres' -count=5`，核对第六笔被拒且未计费 |
| 回退后返回 429 `pending_review_limit` | `NEBULA_UNRESOLVED_EXPOSURE=0`，待核笔数达到旧上限 | 核对待核行数，默认上限为 20 | 只想关闭推荐分项扣款时，保留额度开关默认值，设置 `NEBULA_PENDING_AUTO_SETTLE=0` |
| 北辰响应成功但缓存明细缺失，仍显示待核 | 区间过宽、上界未知、定价超过上界容差，或未获得可信终态用量 | 用调用 ID 查询 `beichen_review_reason`、`unresolved_credit_milli`、`pricing_detail`，核对派发时费率及终态用量 | 设置本机测试库 `NEBULA_TEST_POSTGRES`，运行 `go test -p 1 ./internal/dataplane -run 'TestBeichenPending\|TestBeichenGemini' -count=1 -v` |
| 网关返回 model unavailable | 模型、binding 或 provider 被停用 | 老师启用完整路由链或添加 binding | 按 model -> binding -> provider 顺序排查状态和 priority |
| 上游 429/5xx 后没有切换 | 没有下一个 ACTIVE binding，或流已经开始写出 | 添加低优先级候选；流开始后只能返回当前错误 | 查看候选排序、adapter、priority 和首字节是否已发送 |
| multipart 请求失败 | Content-Type boundary 丢失、文件过大或客户端重复读取 body | 保留原始 Content-Type，调整允许大小 | 检查请求大小和 boundary；不要把文件内容写入日志 |
| Anthropic Messages 返回鉴权错误 | 缺少 `x-api-key` 或 `anthropic-version` | 使用协议原生 Header | 确认网关覆盖上游 key，不透传客户端 key |
| 网关返回 `model_not_allowed`，但模型已在 Key 白名单 | 模型没有与当前入口协议匹配的 ACTIVE adapter Binding | 按入口创建 Responses、Embeddings、Images、Audio、Videos、Realtime、Moderations、Rerank 等独立 Binding | 不要用 `openai_compatible` 代替其他协议；检查 Binding、provider 和 model 状态 |
| Gemini `v1beta` 返回 400/401 | 使用了禁止的 `?key=`、缺少 `x-goog-api-key`/Bearer，或没有 `google_gemini_v1beta` Binding | 使用 Header 携带 Nebula Key，并为模型创建 Gemini Binding | 核对路径 `/v1beta/models/{public_model}:operation`、adapter 和供应商 Base URL；不要记录 key |
| Gemini batch embeddings 上游提示 model 不匹配 | Binding 上游模型名错误，或批量请求内的 model 不是合法 Gemini embedding 模型 | 修正 Binding 的上游模型名 | 网关会统一改写路径和 `requests[*].model`；检查上游返回但不要输出供应商凭据 |
| Realtime WebSocket 连接失败 | token 放在 query、模型不允许、subprotocol 不匹配 | 使用 Authorization 或约定 subprotocol | 检查 Upgrade 响应、模型 query 和白名单；禁止记录 token |
| Codex CLI 普通请求可用但 compact 或 WebSocket 失败 | 缺少 `/v1/responses/compact`、Responses WebSocket Upgrade 未转发、`OpenAI-Beta` 被删除，或 compact output 被代理裁剪 | 使用 `openai_responses` Binding，确认 HTTP 与 WS 都指向支持 Responses 的上游 | compact 必须保留完整 output/`encrypted_content`；检查 `prompt_cache_key`、`previous_response_id` 与 Upgrade Header，不要打印加密内容 |
| Realtime WebSocket 生成失败 | 连接未使用 `/v1/realtime` WebSocket、模型不在 Key 白名单或额度耗尽 | 使用 WebSocket 并在连接 query 指定允许模型；额度耗尽时等待下月或使用 0 倍率模型 | 检查协议 error event 与 request ID；不再提供 WebRTC client secret/calls 路由 |
| Video 创建成功但查询、下载或 remix 404 | Redis 资源路由过期、Video 创建响应没有标准 `id`，或原 Binding/provider 已停用 | 在 TTL 内使用标准 video ID，恢复对应 Binding/provider；必要时重新创建 Video | 检查 `gateway:resource-route:video:{id}` 是否存在；不要把 video ID 当模型重新路由 |
| Backend 启动时报 `GATEWAY_RESOURCE_ROUTE_TTL_HOURS` 配置错误 | Doppler 仍只保存旧视频任务 TTL 字段或值不是正整数小时 | 在 `nebula-api` 对应 config 中写入 `GATEWAY_RESOURCE_ROUTE_TTL_HOURS=24` 并删除旧字段 | 使用按名配置读取确认新字段存在；不要输出整份 secret 配置 |
| `/v1/files`、`/v1/uploads` 或 `/v1/batches` 返回 404 | 这些是有状态资源 API，当前按模型 Binding 的首期网关未实现 | 不要映射到任意默认模型；如需该能力先设计资源归属、权限和生命周期契约 | NewAPI 参考实现同样把多项 Files 路由标为未实现；透明代理无法解决后续 resource ID 应回到哪个上游的问题 |
| 供应商凭据配置后仍不可用 | 加密主密钥不一致或密文损坏 | 恢复原主密钥，无法恢复时重新录入凭据 | 不输出密文/明文；检查 AEAD 解密错误和 key 长度 |
| 修改常用模型后学生页面不刷新 | 全局 SSE 事件丢失或客户端未重新请求模型广场 | 收到事件后重新 GET 模型广场 | 检查 global stream revision；不要把 SSE payload 当完整数据 |
| API 返回 404 但资源存在 | 为防越权，资源不可见与不存在统一返回 404 | 使用正确角色和资源范围 | 检查 JWT user ID、角色、组织/项目成员关系 |
| API 返回未知字段错误 | 控制面拒绝未声明 JSON 字段 | 按 API 文档删除错误字段 | 核对字段拼写和 DTO 版本，不要关闭严格解码 |
| Doppler 启动提示 config 不存在 | config 名拼写错误或当前账号无权限 | 使用真实配置 `nebula-api/dev_personal` 并重新登录 | 运行 `doppler configs --project nebula-api`；只共享 config 名，不粘贴变量值 |
| Doppler 启动提示 placeholder values | `dev_personal` 中仍保存 `.env.example` 占位符或必填值为空 | 在 Doppler 中替换提示的变量；不要写入仓库或聊天 | 只核对变量名与占位状态，不输出真实值；修复后重新运行启动脚本 |
| Doppler 启动提示密钥长度/格式不合格 | JWT/HMAC pepper 少于 32 UTF-8 bytes，或供应商主密钥不是恰好 32 bytes 的 Base64 | 重新生成满足契约的随机值并保存到 Doppler | 只输出长度/格式布尔结果，不输出密钥；三个 JWT/HMAC 值均至少 32 bytes，供应商 key 解码后必须恰好 32 bytes |
| Compose 提示必须通过脚本运行 | 未设置 `NEBULA_VERSION` 或未完成 DSN 安全改写 | 使用 `.\scripts\compose.ps1 ...` 作为唯一入口 | 检查根目录 `VERSION` 是否为 `X.X.X`，不要手工复制 secret 到 shell |
| 启动脚本拒绝 DATABASE_URL | Doppler DSN 指向外部 host、缺少账号密码或数据库名 | 将 `dev_personal` 配置为本地 loopback DSN，或明确确认后调整本地配置 | 只检查 host 类别和字段是否存在，不输出完整 DSN |
| `postgres` 一直 unhealthy | 命名卷中的初始化账号与当前 Doppler DSN 不一致、卷损坏，或旧宿主机 libseccomp 将 PostgreSQL 新 syscall 错误返回为 `EPERM` | 恢复创建该卷时的 Doppler 数据库配置；生产必须保留版本化 ENOSYS seccomp profile；需要清空卷时先获得明确授权 | 查看 postgres 日志；若出现 `pwritev2`/`Operation not permitted`，运行隔离的 `pg_test_fsync` 验证 profile，禁止使用 `seccomp=unconfined` |
| `redis` 一直 unhealthy | Redis 启动失败、AOF 损坏或 Docker 磁盘异常 | 根据 Redis 日志修复；不要擅自删除数据卷 | `.\scripts\compose.ps1 logs --tail 100 redis` 和 `docker system df` |
| `migrate` exited (1) | PostgreSQL 未就绪、凭据不匹配、`citext`/Ent schema 创建失败 | 修正本地 DSN或数据库权限后重建一次性 service | `.\scripts\compose.ps1 logs --tail 100 migrate`；确认目标是 Docker 本地卷 |
| `backend` 不启动 | migrate 未成功、Redis 不健康或 Doppler 必填配置错误 | 先恢复依赖，再重新 `up -d` backend | 按 postgres -> redis -> migrate -> backend 顺序查看 `ps` 和日志 |
| `maintenance` 反复记录 `last_success_at` 类型错误，或一直处于 `health: starting` | heartbeat SQL 未显式约束 text/timestamptz 参数类型，或 maintenance 继承了只适用于 backend 的 HTTP healthcheck | heartbeat 参数显式使用 `::text`/`::timestamptz`；maintenance service 禁用继承的 HTTP healthcheck并由 Docker running 状态监控 | 运行 `go test ./internal/usage -run TestRunMaintenanceUsesTypedHeartbeatParameters`；查看 maintenance 日志确认不再出现 `SQLSTATE 42804` |
| backend 报测试账号配置或角色冲突 | 某组 `TEST_*_{NAME,EMAIL,PASSWORD}` 未成套填写、格式不合法，或该邮箱已属于另一角色 | 补齐 Doppler 三元组或使用角色正确且唯一的测试邮箱；禁止修改既有用户角色 | 根据错误中的变量前缀或通用角色冲突定位，不打印 Secret；生产必须配置三组学生和三组导师 |
| `/health/live` 成功但 `/health/ready` 503 | 进程存活但 PostgreSQL 或 Redis ping 失败 | 修复显示为 unavailable 的依赖 | 响应只给依赖类别；进一步查看对应容器日志 |
| 公网 API 返回 Cloudflare `530`，Tunnel 控制台显示 `Active replicas: 0 / Down`，浏览器同时报告 CORS 缺少 `Access-Control-Allow-Origin` | backend 正常但 cloudflared 到 Cloudflare edge 的长连接不可达；CORS 报错只是 530 页面没有业务 CORS Header 的二次表现 | 恢复服务器到 Cloudflare edge 的直连网络；服务器等待 edge 注册，GitHub Runner 从公网验证 readiness | 检查 `docker compose -p nebula-api -f compose.production.yaml logs --tail 100 cloudflared` 和 `https://api.lyn91r.cn/health/live`；Tunnel 日志应出现 `Registered tunnel connection`，不得只检查容器 running |
| 8080 端口被占用 | 本机其他程序已监听 `127.0.0.1:8080` | 停止冲突程序；端口契约变更需同步 Compose 和文档 | `Get-NetTCPConnection -LocalPort 8080 -State Listen` |
| 修改代码后仍运行旧行为 | 未重建镜像或 `VERSION`/image tag 与预期不一致 | 执行 `.\scripts\compose.ps1 up -d --build`，发布时按 SemVer 更新 `VERSION` | `.\scripts\compose.ps1 images` 与 `docker image inspect nebula-api-backend:<version>` |
| 2C2G 服务器部署时 CPU、内存或磁盘 I/O 突增 | 首次冷构建、Go 版本或依赖变化导致缓存失效，Go 编译仍需计算资源，或 Compose 同时 pull/start 多个 service | 生产 Dockerfile 固定单核 Go 构建；BuildKit 使用稳定命名的 module/build cache 复用未变化 package；部署脚本以 `COMPOSE_PARALLEL_LIMIT=1` 逐个 pull，并按 postgres -> redis -> migrate -> backend -> maintenance -> cloudflared 串行启动，每阶段间隔十秒 | 运行 `go test ./scripts` 检查缓存契约；Actions 日志应显示 `persistent BuildKit caches` 且同一时间只有一个 pull/build/start 操作；服务器可用 `docker stats --no-stream`、`uptime` 和 `iostat -xz 1 3` 观察，不要并发重跑 workflow |
| 后续发布仍反复下载全部 Go module 或全量编译 | Docker builder 首次运行、BuildKit 未启用、Go 版本/`go.mod`/`go.sum` 变化，或 builder cache 被人工清理 | 保持 `DOCKER_BUILDKIT=1` 和 Dockerfile 中的 `nebula-go-mod`/`nebula-go-build` cache mount；允许首次冷构建完成，后续构建自动复用 | 使用 `docker system df -v` 只读检查 Build Cache；不得在部署脚本中自动执行 `docker builder prune`，磁盘确有压力时必须先确认清理范围并获得授权 |
| main push 后没有部署 | workflow 未进入 `production` environment，或 GitHub 缺少 `DOPPLER_TOKEN` | 检查 Actions 运行记录和 environment secret；不要把 token 改写到 workflow | `gh run list --workflow deploy.yml` 和 `gh secret list --env production`，只核对名称 |
| 部署在读取 Doppler 配置时超时 | GitHub Runner 或云服务器到 Doppler API 的网络波动；旧的 CLI 完整配置下载特别容易受 dynamic secrets 影响 | 两端均使用 `curl` 的按名 REST 查询并对同一请求最多重试五次；生产服务器必须能直连 Doppler HTTPS API | 查看 `Sync source and deploy` 日志并在服务器执行不输出 secret 的 Doppler API 连通性检查；不要将生产配置复制到 GitHub secret |
| 部署在 SSH 校验阶段失败 | Doppler 主机信息已更新但 `DEPLOY_SSH_KNOWN_HOSTS` 仍是旧主机公钥，或密码认证被禁用 | 在可信通道核对新服务器公钥并同时更新五个 `DEPLOY_SSH_*` 变量 | 不得关闭 `StrictHostKeyChecking`；不要在日志打印密码或完整 Doppler 配置 |
| 远端镜像未更新 | 源码同步失败、服务器本地构建失败、磁盘不足或 Docker 服务异常 | 修正 SSH/Docker/构建网络问题后重新 push 新 commit | 检查 workflow 的 `Sync source and deploy` 日志、`docker image inspect nebula-api-backend:<version>`、`df -h /opt` 和 `docker system df`；不得删除无关项目镜像或卷 |
| migrate 成功但应用健康检查超时 | backend 配置错误、依赖未健康、端口冲突或 frontend 未启动 | 按 postgres -> redis -> migrate -> backend -> frontend 顺序定位 | `cd /opt/nebula-api && docker compose -p nebula-api ps`；日志中不得输出 DSN 或 secret |
| 云服务器 8080/8081 从公网不可达 | Compose 按安全契约只绑定 loopback | 使用 SSH tunnel；配置域名、TLS 和反向代理应作为独立部署变更 | 服务器执行 `ss -lntp '( sport = :8080 or sport = :8081 )'`，应只看到 `127.0.0.1` |
| DataGrip 无法连接生产 PostgreSQL | PostgreSQL 未加入专用 `management` 网络导致 Docker 未创建 loopback 端口转发，或 SSH tunnel 配置错误 | 保持 PostgreSQL 同时加入 `data` 和仅该服务使用的 `management` 网络，映射服务器本机 `127.0.0.1:15432`，并在 DataGrip 启用 SSH tunnel；不要连接公网 5432 | 服务器执行 `ss -lntp '( sport = :15432 )'`，应看到 `127.0.0.1:15432`；DataGrip General 使用 `127.0.0.1:15432`，SSH 使用服务器 22 端口 |
| 前端打开后白屏 | 前端生产构建失败、静态资源未进入镜像或路由脚本异常 | 先完成 typecheck/build，再重建 frontend 镜像 | `cd frontend; npm run typecheck; npm run build`，浏览器查看 Console 与 Network，不粘贴 token |
| 页面或表格请求期间没有加载反馈，或骨架跑到页面右上角 | 页面绕过官方 API client、局部初次加载状态被提前标记为完成，或把骨架误放入全局网络层 | 所有控制面请求使用类型化 API client；全局层只允许顶部进度条，页面/表格骨架必须由对应内容区域的 `LoadingRegion` 承载；不要用空列表冒充加载完成 | 浏览器 Network 节流后依次访问所有侧栏页面，确认骨架位于对应列表/表单容器；运行 `cd frontend; npm run test -- src/components/NetworkLoadingLayer.test.ts src/components/LoadingRegion.test.ts src/composables/useLoadState.test.ts` |
| 请求完成后加载动画一直不消失 | 请求计数没有在异常路径 `finally` 中递减，或页面启动了未结束的普通 HTTP 请求 | 保证成功、失败和 `401` 重试都成对维护计数；持续连接必须使用独立 SSE/WebSocket 通道，不能计入普通请求 | 检查 Network 中未结束请求及 `networkActivity` 调用链；运行 API client 回归测试，不通过吞错或强制归零掩盖问题 |
| 快速重复点击造成多次提交 | 写操作没有局部 pending key，按钮只依赖全局提示但仍可点击 | 使用 `LoadingButton` 和 keyed pending action，在 promise 完成前禁用同一操作 | Network 中同一资源操作只能有一个在途请求；运行 `cd frontend; npm run test -- src/composables/usePendingActions.test.ts` |
| 导师从“审核密钥”点击“项目管理”后整个工作台变空 | 导师与老师的项目页面使用了相同 Vue Router `name`，后注册路由移除了先注册的 `/mentor/projects` 记录 | 所有工作区路由使用全局唯一技术名称，中文标题通过 `meta.title` 展示；禁止复用展示文案作为路由名 | 运行 `cd frontend; npm run test -- src/router/index.test.ts`，并在浏览器确认 `/mentor/projects` 的 `route.matched` 非空且项目接口已发出 |
| 老师模型管理的新增模型弹窗中 checkbox 与文字错位或纵向间距异常 | 全局普通输入框样式覆盖了 checkbox 的宽度、显示方式和上边距 | 使用模型表单专用 checkbox 选项布局，并重新部署前端 | 打开老师 `模型管理 -> 新增模型`，确认输入/输出模态及“全局常用模型”的 checkbox 与文字同一行、间距一致 |
| 老师配置 Binding 后模型卡片仍显示“待配置”且同时显示“路由就绪”小泡泡 | 卡片直接渲染数据库 `status`，又重复渲染派生的 `route_ready`；`pending_configuration + route_ready=true` 的展示语义没有统一 | 卡片将该组合展示为“已就绪”，移除“路由就绪/未就绪”小泡泡；数据库状态仍由模型启用/停用契约控制 | 配置 ACTIVE Binding 和 ACTIVE provider 后刷新老师模型列表，确认卡片只有“已就绪”状态徽标；运行 `cd frontend; npm run test -- src/utils/models.test.ts` |
| 直接打开或刷新 `/login`、`/teacher/...` 返回 Vercel `404: NOT_FOUND` | Vercel 未读取 `frontend/vercel.json`，通常是 Root Directory 不是 `frontend` 或新配置尚未部署 | 将 Vercel Root Directory 设置为 `frontend` 并重新部署当前版本 | 直接请求 `/login` 和 `/teacher/key-reviews`，确认 HTTP 200 且响应为 `index.html`；不要改用 hash 路由或增加后端旧路由 |
| 页面刷新后回到登录页 | refresh Cookie 不存在、Path/Secure/SameSite 不匹配，或 Redis session 已失效 | 修正 Cookie 与同源部署配置后重新登录 | 检查 `/api/v1/auth/refresh` 状态和 Set-Cookie 属性；前端不会从 JS 读取 refresh token |
| 登录后刷新页面被重定向到登录页，且 refresh Cookie 原本有效 | 路由守卫与 `App.vue` 同时调用 bootstrap，单次轮换 refresh token 被并发使用，后端将第二次请求识别为重用并撤销 session family | 由 auth store 提供 single-flight bootstrap，并让路由守卫复用同一个 Promise；不要在多个启动入口重复调用 refresh | 运行 `cd frontend; npm run test -- src/store/auth.test.ts`，浏览器 Network 中刷新页面应只有一个 `/api/v1/auth/refresh` 请求 |
| 多个请求同时 401 导致反复刷新 | API client 未共享 refresh 请求，或 refresh token 被并发轮换重用 | 使用前端内置 single-flight client，禁止页面直接调用 fetch | Network 中应只有一个并发 refresh；若 session family 被撤销则重新登录 |
| 前端提示接口 404 | 未通过 Vite/Nginx 代理访问、路径写成旧路由或 backend 未启动 | 使用相对 `/api/v1` 路径并恢复 backend | 检查浏览器请求 URL、`frontend/nginx.conf` 和 Compose 网络，不增加旧路由别名 |
| 注册成功收到验证码但提交返回 `VALIDATION_ERROR` 且提示未知字段 | 注册页面把 `role`、密码确认字段等 UI 状态一并发送；后端注册 DTO 只接受 `name`、`email`、`password`、`verification_code` 并严格拒绝未知 JSON 字段 | API client 现在显式构造四个允许字段；更新前端构建后重新注册 | 浏览器 Network 查看请求 JSON 的字段名，不要提交 `role`、`confirm` 或其他表单状态；错误提示会指出具体字段 |
| 前端错误提示出现 `body: 包含不支持字段` 或英文原始错误 | 客户端直接展示 Gin/JSON 解析器原文，未按错误码和字段原因翻译 | 使用统一 API error 文案映射；校验错误显示具体字段和可执行处理方式 | 检查响应 `error.code`、`error.details` 和 `request_id`；不要把完整请求头或 token 粘贴到工单 |
| 前端 SSE 无状态更新 | 使用 EventSource 无法带 Bearer，或 fetch stream 中断 | 使用内置 fetch streaming，重连后重新拉取列表 | 检查 `/api/v1/events` Authorization 与响应 Content-Type；不要把 token 放 query |
| 领取弹窗关闭后找不到完整 Key | 明文按契约只在首次响应与当前内存弹窗出现 | 使用首次领取时复制的值；遗失后只能重新申请 | 数据库和浏览器存储都不应存在明文，禁止增加“再次显示”功能 |
| 8081 端口被占用 | 本机其他进程占用前端入口 | 停止冲突进程或按正式变更流程修改端口和文档 | `Get-NetTCPConnection -LocalPort 8081 -State Listen` |
| DeepSeek harness 已完成回答，调用审查仍是 `upstream_failed` 且没有 token | 客户端携带 `gzip`/`br` 时，`deepseek-flash` 的 SSE 会混入二进制；网关原样转发该压缩头，流里没有可解析的 `data:` 事件和 `usage` | 向上游请求 `Accept-Encoding: identity`。学生提问的失败轮次仍保留；历史失败记录没有用量，不能补记 token | 运行 `go test ./internal/dataplane -run 'TestUpstreamRequestDisablesResponseCompression|TestDeepSeekAnthropicUpstreamUsesOfficialPrefix'` |
| 调用审查出现成串 `upstream_failed`，用户 prompt 为「—」或客户端标题/时钟文本 | 上游以 HTTP 200 结束 SSE，且在正常 `data:` 事件前断开；这类请求没有学生句子，也没有会话编号，每次重试都新开一轮 | 审查列表只保留存有学生提问的轮次。空标题，以及「Generate the session title」和「Time sampled while preparing turn」开头的客户端请求不列出。学生提问失败的轮次仍保留 | 运行 `go test ./internal/usage -run 'TestHumanTurnTextDropsClientBookkeeping|TestCallReviewPromptSQLHidesEmptyAndClientBookkeeping'`；不要把响应正文补进审查列表 |
| 同一句用户提示词换了模型，调用审查只显示先用的模型 | 没有新提示词的后续调用只在同一模型的行里查找，换模型后新开了空标题行，审查列表把空标题丢掉 | 没有新用户提示词时，换模型的调用留在已有提示词那一行。这一行按实际调用顺序列出用过的模型。新的用户提示词仍另开一行 | 运行 `go test ./internal/usage -run 'TestModelSwitchWithoutNewPromptJoinsThePromptRow|TestModelSwitchWithNewQuestionOpensARow'` |
| `down` 后数据仍存在 | 命名卷按设计持久化 | 重新启动即可恢复；不要把持久化误判为清理失败 | `docker volume ls --filter name=nebula-api_`；`down -v` 会永久删除数据 |

## 4. 分层排查顺序

1. 使用 response Header 或 envelope 中的 `request_id` 关联日志。
2. 检查请求方法、标准路由、Content-Type 和认证 Header。
3. 检查当前用户状态、角色和资源归属。
4. 检查 PostgreSQL 权威状态及合法状态迁移。
5. 检查 Redis 中的短期状态、TTL 和 stream。
6. 检查供应商/模型/binding 三层启用状态。
7. 最后检查上游连接、超时、429/5xx 和协议响应。

禁止通过关闭权限校验、吞掉错误、手工修改单张表、返回完整 secret 或启用旧路由来“临时解决”问题。

## 5. 新功能同步模板

每个新增功能至少在本文件补充以下内容：

```markdown
### 功能名称

- 测试范围：
- 正常路径：
- 权限/安全边界：
- 并发或事务边界：
- 常见现象：
- 根因：
- 解决方案：
- 排查命令或观察点：
- 不得记录的敏感信息：
```

### 缺终态待核积压

先在供应商对账页回放，查看全量 `terminal_missing` 和策略开关。尚在宽限期的调用继续等待上游；到期后可等下次 60 秒扫描，也可确认「立即处理缺终态」。若仍积压，检查策略是否为 `hold`、调用结束时间、批量上限及服务日志中的调用 ID。数据库错误会回滚该笔，其他调用继续处理。已结清但积分为 0 时查看 `pricing_detail.reason`，并核对保存的计数与派发时报价。

设置本机测试库 `NEBULA_TEST_POSTGRES` 后运行 `go test -p 1 ./internal/dataplane -run TestTerminalPending -count=1 -v`。测试核对 15 笔样本的实扣和零元核销、宽限期、暂停策略、并发、迟到用量和月度账本，并单测 OpenAI 缓存包含在输入总数中的计费口径。教师端确认流程用 `npm test -- src/views/teacher/BeichenReconcile.test.ts` 验证。
