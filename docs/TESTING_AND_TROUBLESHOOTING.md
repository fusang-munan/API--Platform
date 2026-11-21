# Nebula 测试与故障排查

学校证书通知使用 `Lab backend TLS integration` 中的本地 SMTP 接收器验收。通过维护 HTTPS 提交单份已批准申请，验证邮件只含实际版本、入口和监听证书的指纹，重复请求和后端重建均不会再发。202 表示已处理公开请求；若尚未收信，请先检查收件箱和垃圾箱、学校发件地址、批准的版本及到期时间。勿重复使用注册、邀请或密码找回流程查询投递。

## 查询某一笔学生扣分

保存学校网关返回的 `X-Request-ID`，用发起调用的学生账号查询 `GET /api/v1/student/usage/calls/{request_id}`。404 时核对账号、请求编号和实际供应商；本人重复使用编号也会得到 404。收到 2xx 推理响应后仍可能暂时没有北辰流水，保留原编号稍后重查即可。一次超时也可能已入账，应先读同一编号的回执。金额为 `null` 表示该项尚无数字可核对。

执行 `go test ./internal/usage ./internal/api/http -run StudentCallReceipt -count=1` 检查回执、输入限制和匿名鉴权。配置隔离 PostgreSQL 后，`go test ./internal/usage -run TestBeichenLedgerPostgres -count=1 -v` 同时覆盖本人范围、编号歧义及精确金额。

## 学校网页能打开，维护用 HTTPS 握手失败

教师在模型页确认已发现的模型、当前倍率与启用权限，在项目中给学生分配额度、查看开销。维护者可从学校已核实的 HTTPS 入口读取收件公钥，随下一次 GitHub 自动部署交付加密配置。北辰网页同步需要维护者先运行 `scripts/capture-beichen-session.ps1`，亲自完成一次登录；程序只交付加密后的访问令牌和续期令牌。若工具提示账号响应没有邮箱，核对登录页已进入个人面板后重试，不要手工把令牌粘贴到聊天。

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
- 模型广场必须展示 `credit_multiplier`；老师模型配置在已有倍率被改为不同值时必须要求 `multiplier_change_reason`，首次从 NULL 设置默认倍率不要求原因且不写倍率变更审计。Key 申请、导师初审、老师终审分别验证三阶段额度字段。
- 老师模型向导只通过顶部节点导航：填写模型 ID 和名称后第二节点解锁，第一步不显示“下一步”。进入第二步必须立即显示一次“一键填充”询问；选择否后保留描述、类别、文本模态、128K/128K/4K 容量和 1.00x 倍率默认值。选择是时模型 ID 与上游模型名默认来自第一步；Probe 必须自动请求 `/v1/models`，将真实 description、tags、model type、endpoint types 和按优先级推断的容量写回第二步，成功后关闭子弹窗且不渲染响应 body。Binding 加号和编辑按钮必须打开 Portal 居中弹窗。
- 老师终审成功路径必须验证项目月 bucket 更新同时绑定 `project_id` 与当前上海月份；SQL 占位符与实参数量不一致时，pgx 会在事务内拒绝执行并完整回滚。前端遇到终审依赖错误时必须关闭确认弹窗，明确说明额度和申请状态未变更，并展示响应 `request_id` 供管理员排查。
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
4. 学生选择组织、项目和至少一个模型申请 Key。
5. 导师初审后，状态从 `pending_mentor` 变为 `pending_teacher`。
6. 老师确保所有模型和 binding ACTIVE 后终审，状态变为 `approved`。
7. 确认学生已自动加入组织和项目。
8. 学生首次领取，取得一次性明文并转为 `active`；重复领取必须失败。
9. 使用 Key 调用 `GET /v1/models` 和一个允许模型；调用白名单外模型必须失败。
10. 导师撤销 Key，后续网关调用必须失败，但学生成员关系保留。

### 2.7 控制台人工验收

1. 分别用学生、导师、老师账号登录，确认默认落点、侧栏权限、手机导航抽屉和退出登录；导师从“审核密钥”点击“项目管理”后必须保留完整工作台并显示项目列表或明确空状态。
2. 学生选择组织后确认停用/无导师项目可见但禁选；切换组织确认会清空后续草稿。
3. 没有生效 Key 时，从模型广场点击「申请密钥」，在首次申请中选择广场模型。
4. 导师在最早优先队列完成通过和带原因驳回；老师检查逐模型路由后终审。
5. 学生观察 SSE 状态变化：只有已有 Key 的状态真正变化时自动打开该 Key 详情；首次进入、普通刷新、SSE 重连和新提交不会自动弹窗。领取时验证未复制关闭二次确认、复制失败手动选择及关闭后 secret 清空。
6. 用生效 Key 在模型广场点击「添加到我的 Key」，核对该模型显示「已同步」，并出现在「申请密钥」页的「已授权模型」列表。选择不同项目的 Key 时，核对授权列表与所选 Key 对应。
6. 老师验证导师候选搜索分页、组织/项目启停、凭据独立替换、模型 nullable 数值和 Binding 编辑。
7. 缩窄至 375px 宽度并仅使用键盘完成认证、申请、审核和弹层关闭；开启 reduced motion 后确认无位移、缩放、旋转或 shimmer 动画，但加载文字、禁用态和错误信息仍然可见。
8. 对每个角色依次打开所有侧栏页面：GET 期间应显示顶部加载进度和数据骨架；使用局部 `LoadingRegion` 的复杂列表在初次加载时不得先闪现误导性的空状态，刷新时继续保留已有内容；登录、注册、退出及使用局部 pending 的写操作按钮应显示处理中并禁止重复提交。

## 3. 常见问题矩阵

### 学生 Key 新增模型授权

设置 `NEBULA_TEST_POSTGRES` 为隔离的本机测试库，运行 `go test ./internal/dataplane -run TestStudentKeyModelAuthorizationPostgres -count=1`。该测试使用假上游，覆盖原 Key 即时列出与调用新增模型、重复及并发添加、审计、整批回滚和越权拒绝。前端运行 `npm test -- StudentModels StudentKeys`，覆盖无生效 Key 时的申请入口、同步、失败原因和重试。

- 提示没有可用线路时，核对模型状态、报价，以及至少一条线路的供应商、凭据和目录条目是否有效。
- 已加入广场但 Key 同步失败时，按错误原因处理后对同一 Key 重试。若客户端仍显示旧列表，用同一把 Key 重读 `GET /v1/models` 核对服务端授权。

### 北辰接入验收

1. 在隔离的本机 PostgreSQL 测试库运行 `go test ./...`。设置 `NEBULA_TEST_POSTGRES` 指向回环地址上的 `nebula_test` 后，可验证报价快照、小数精度、重复流水、延迟结算及并发超额。前端运行 `npm run typecheck`、`npm run test` 和 `npm run build`。
2. 从已核实的学校 HTTPS 入口核对镜像版本与收件公钥。教师页应直接显示同一模型的首选倍率和启用状态。目录有模型但暂缺倍率时要显示待同步。核对价格时查看同一把北辰 Key 的分组、基础输入输出价、缓存价格、倍率和适用时段。
3. 校方准备好真实验收后，使用同一把学生 Key，四条渠道各发一笔短文本请求。依次记录学生可用额、学校请求编号、上游响应编号，以及北辰官网使用明细的数字 Key ID、模型、用量和实扣。收到响应但账单尚未出现时保留待核，继续读取相同编号。
4. 校内扣分应等于该次基础积分乘发件时的学校系数。把北辰实扣乘 100 换为积分后，再与校内扣分相减。此差额可为正或负。缺价格的调用可能靠官方流水补记，验证时也要记录基础积分来源。
5. 后台目录、价格及流水均报超时时检查学校后端的 `HTTPS_PROXY`、`NO_PROXY` 和北辰官网授权状态。若提示官网要求额外登录验证，学校的网页登录仍待授权。在个人浏览器打开北辰官网只会影响该浏览器的登录状态。核对后台读取是否恢复时，查看「供应商管理 → 北辰 → 查看模型」中的网页报价及逐笔流水成功时间。
6. 项目已经扣分但所选日期没有逐笔记录时，先核对北京日期和网页读取进度。最后成功读取一页和已读到末页分别反映最近活动和完整进度。没有成功时间时，空列表只能说明学校尚未归档该日流水。账单已有记录而学校扣分栏待核时，可在「项目监控」核对该学生的调用。使用隔离的本机测试库运行 `go test ./internal/beichen -run TestReceiptSweepPostgresG3 -count=1`，覆盖上游更正金额、同一时间分页、实扣为零和无法确认归属的记录。

### 北辰教师端用量、定价与对账

#### 官网目录模型数少于网页渠道页

打开「模型管理 → 北辰」，检查「官网目录」的最近完整读取时间，并展开具体模型的「目录来源与分组」。收到官网错误时，页面会保留已存的型号并提示待更新。官网有 Claude 但接替 Key 的目录没有时，核查该行的平台、分组和 CC1 新 Key，选择对应 Key 重扫。重扫成功后仍为零时，核对新 Key 的上游权限、分组和模型原名。出现多个近似名称时，可为正确的上游名称配置教师别名。

配置隔离的本机 `NEBULA_TEST_POSTGRES` 后运行 `go test ./internal/beichen -run 'TestBeichenWebsiteDirectoryPostgres|TestBeichenDirectoryNameMatching|TestBeichenPanelPricesMatchEquivalentKeySpelling' -count=1 -v`。测试仅用构造数据和假上游，覆盖多平台完整入库、空列表、空价格、名称变体和教师别名、仅官网型号、换 Key 后重新匹配、部分更新、失败保留和退避。运行 `npm test -- BeichenSchoolModels TeacherProviders` 检查教师页显示和指定 Key 重扫。

从仓库根目录运行 `go run ./cmd/beichen-crawl --fixture ./internal/beichen/testdata/website-directory.json`。构造样本应读到 9 个平台、20 个去重型号和 5 个分组。可给 `--fixture` 传入完整的官网响应对象或其中的渠道数组。JSON 输出中的 `complete` 为 `true` 时，`model_count` 和 `expected_model_count` 一致，且 `failures` 为空。每个型号的 `sources` 保留各平台和分组，`price_complete` 表示该来源的 Token 报价具备当前规则所需的字段。明示零单价按数字处理，缺失、空白和 `null` 会保留型号。附带费用被忽略时，`ignored_fields` 给出字段名。

用 `go test ./cmd/beichen-crawl ./internal/beichen -run 'Test(BeichenCrawl|CrawlCommand|BeichenWebsiteDirectoryPostgres)' -count=1 -v` 复查退出码、完整计数、单平台损坏、空价与零价、教师别名和抓取记录入库。看到 `complete=false` 时，用 `failures[].path` 和 `reason` 定位错误。网页不可读时教师页上次抓取可显示 0，先前已保存的目录仍可用于查看。需要核对 Key 时加 `--match-db` 并设置 `DATABASE_URL`，`key_matching=not_checked` 表示本轮没有执行这项核对。`key_matching=failed` 时查看 `key_match_error`。

#### 改分组后仍显示旧倍率或缺报价

旧模型页的定向重扫只读模型清单，旧概览的分组取自上次采价或预配映射。新版在「用指定 Key 重扫」后读取当前 Key 账单。查看「Key 分组与倍率」中的生效倍率、分组与上次成功时间。若显示「倍率已陈旧」，查看该行的账单错误并重扫。第三次连续失败会同时写入服务告警。待核的跟随价调用可由通过校验的北辰回执补记。

行内出现「分组报价需核对」时，对照「官网分组 X 价」与「Key 分组 Y 价」及各自倍率。显示「Key 分组不含该模型」时，可在北辰为该 Key 选对应分组，或从供应商管理换用对应 Key，再重扫该 Key。

设置隔离的本机 `NEBULA_TEST_POSTGRES` 后运行 `go test ./internal/beichen -run TestBeichenBillingGroupRefreshPostgres -count=1 -v`。构造数据覆盖 0.15 到 0.6 的切换、同平台不同分组单价、迟到的旧调用、仅高峰规则变更、官网读取失败、过期账单响应和连续失败告警。教师页运行 `npm test -- BeichenSchoolModels` 核对倍率与分组差异。

#### 探活与目录差异

设置 `NEBULA_TEST_POSTGRES` 为隔离的本机 `nebula_test` 后运行 `go test ./internal/beichen -run 'TestBeichenProbeAndCatalogPostgres|TestSingleAndBatchEnableUseSamePricesPostgres' -count=1`。探活测试使用模拟 HTTP 响应，核对协议请求的输出限制、重复排队、每小时触发、结果脱敏和 Key 轮换后的隔离。前端运行 `npm test -- BeichenSchoolModels TeacherProviders` 验证健康项、探活状态和目录差异。

- 「待探活」时先核对该 Key 是否启用、目录是否已同步，以及模型是否登记了可限制为 1 个输出 Token 的生成协议。检查 `BEICHEN_PROBES_ENABLED` 后可点「立即探活」，再用教师探活 GET 查看状态、错误代码及 HTTP 状态。北辰若拒绝 1 Token 限制，失败结果会保留，方便核实当前协议及其参数要求。
- 「已启用 · 线路异常」时查看模型下的说明，并在供应商页核对 Key 状态和目录差异。目录差异显示「待核对」时，先核实网页读取授权和渠道分组，再同步。纯 Token 条件价需有有效的输入、输出费率及可判定的档位，才能计入报价覆盖。

用 `go test ./internal/usage -run 'TestBeichenTokenQuoteBlocker|TestEvaluateBeichenConditional' -count=1` 验证空计价模式、非 Token 报价、分档缺价及结算。为本机测试库设置 `NEBULA_TEST_POSTGRES` 后，上面的启用测试还会核对概览、单个启用和批量启用的报价提示是否一致，以及线路不可用时是否优先显示线路原因。

纯文本模型探活通过后仍显示「官网报价格式无效」或「非 Token 计价」时，先看 `price_blocker.message` 的字段名。旧解析会拒绝空字符串，并把非 Token 字段的零值当作有费用。运行 `go test ./internal/beichen -run 'TestPanelMixedPriceFixture|TestPanelMixedPricesActivationAndSettlementPostgres' -count=1 -v`，用构造的面板 JSON 和隔离的本机数据库复查。测试覆盖 gpt-6.1-sol 的固定价、gpt-6.2-sol 的两个 Token 档位、缺价档的提示与待核账、两种启用入口及扣分明细中被忽略的字段。

后端使用隔离的本机 PostgreSQL。新建的测试库需在 `public` 安装 `citext`、`pg_trgm` 和 `pgcrypto`。全量测试的网关用例可使用下列本机端口。

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

### 北辰 Token 缺项与逐笔差额

旧版网关会把含部分计数的调用标为 `missing`，旧报表会跳过其中保存的分项。调用数和已扣积分来自账务记录，因而可能同时出现。使用教师调用对账接口，按同一账日核对每行的原始计数、`usage_state` 与 `missing_token_reasons`。Chat 流式用量取上游使用量事件。Responses 流式和 WebSocket 使用对应响应的终止用量。Anthropic 合并开始事件和后续累计计数。Gemini 原生生成回报没有独立的缓存写入分项。

逐笔核对时用 `base_difference_credits` 比较基础积分与回执实扣，用 `difference_credits` 单独核对独立计价与实扣。学校系数与额度封顶可以另用 `school_multiplier`、`student_credits`、`school_absorbed_credits` 复核。存在差额时结合本地和回执 Token、价格档位、渠道倍率版本、推理强度和缓存费率核对。日账缺失时查看逐笔证据，区间利润保持未知。

在上述本机测试库运行 `go test ./internal/dataplane -run 'TestBeichenUsageProtocolMatrix|TestBeichenPostgresGatewayReconciliation' -count=1 -v`。假上游的 41 笔调用覆盖完整用量、缺缓存明细及缺用量，且检验逐笔回执补记、回执重放和差额精度。完整检查使用 `go build ./...` 与 `go test ./...`。

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
| 自定义模型提交后无法终审 | 新模型处于 `pending_configuration` | 老师补全模型、至少一个 ACTIVE binding 和 ACTIVE provider | 终审返回的 `model_ids` 是未就绪集合 |
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
| 老师终审返回 `503 DEPENDENCY_UNAVAILABLE`，页面提示终审未完成 | 终审事务的数据库操作失败；曾出现项目月 bucket 更新 SQL 使用 `$1..$4` 但只传 3 个参数，pgx 报 `mismatched param and argument count` | 保持终审事务向 bucket 更新完整传入额度、时间、项目 ID 和月份；失败时事务必须回滚，修复后重新终审 | 从响应复制 `request_id`，在 backend 日志中按该 ID 定位真实错误；运行 `go test ./internal/usage -run TestApproveKeyBindsProjectBucketMonth` 和 `cd frontend; npm run test -- src/api/client.test.ts` |
| 老师驳回申请返回原因缺失 | 驳回请求没有填写非空审核意见；通过请求的审核意见可以省略 | 在驳回弹窗填写具体原因；通过时可以留空意见 | 期望错误码为 `REJECTION_REASON_REQUIRED`；通过请求空 body 应返回 `204` |
| 老师审批导师项目申请提示导师关系异常 | 审批时发现导师未加入项目所属组织，或导师已经是项目成员；空 body 则可能是请求体解析错误 | `MENTOR_NOT_IN_ORGANIZATION`：在“组织管理”重新分配导师；`MENTOR_ALREADY_PROJECT_MEMBER`：无需重复审批并刷新列表 | 查看响应 `error.code`；核对 `organization_members`、`project_members` 和申请状态 |
| 老师看不到 ACTIVE Key | 这是首期权限边界，不是数据丢失 | 由负责导师查看和撤销 | 老师接口只暴露 `pending_teacher` 摘要 |
| 终审后学生未加入组织/项目 | 终审事务回滚或数据库约束失败 | 修复根因后重新执行合法终审；禁止手工只改 Key 状态 | 同时检查 Key、两张 member 表和 audit；四者必须原子一致 |
| 领取接口没有再次返回完整 Key | 明文只在首次领取返回一次 | 创建新申请，不支持找回 | 只能使用 prefix 定位；任何人都不应从数据库恢复明文 |
| 领取时报 `KEY_ALREADY_CLAIMED` | Key 已是 ACTIVE 或并发请求已有一个成功 | 使用首次响应中的明文 | 检查 `claimed_at` 和 prefix，不要要求后台重显 |
| 网关返回 invalid API key | Key 不存在、HMAC pepper 变化、状态非 ACTIVE | 使用正确 Key；恢复原 pepper 或重新申请 | 只记录 prefix；检查部署间 `API_KEY_HASH_PEPPER` 是否一致 |
| 网关返回 model_not_allowed | 请求模型不在该 Key 的规范化白名单 | 使用 `GET /v1/models` 返回的模型 | 核对 `api_key_models`，不要依赖客户端缓存 |
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
