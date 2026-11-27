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

学生填组织、项目、Key 名称和申请额度。负责该项目的导师批准后，学生领取 Key，可随时为原 Key 添加或移除授权模型。个人模型库用于收藏。维护中的模型暂停新增授权，待验证模型可添加，接口会提示探活状态。已有授权保留在 Key 详情中。网关目录默认展示可用和待验证的模型，查询参数 `include_unavailable=1` 可查看维护中的授权。

## 北辰接入

学校通过原有 GitHub 自动部署接入北辰的四个渠道。教师在「模型管理」查看当前可用模型、启用状态和首选线路的生效倍率，在项目里给学生分配月度额度，查看每人的使用额和各模型开销。学生领用自己的学校 Key 后即可在获批额度和模型范围内调用。

学校首次更新后会生成专用于接收北辰配置的密钥。维护者用校方已确认的 HTTPS 证书运行 `scripts/seal-beichen.ps1 -Certificate <证书路径>`，本地加密四把渠道 Key。学校的北辰网页会话在「供应商管理 → 北辰 → 网页登录」保存，由服务器直接用于定时抓取。渠道 Key 的部署步骤见[学校入口说明](docs/FRP_PUBLIC_ACCESS.md)。

后台约每 15 分钟读取北辰登录网页的 Key 清单和可用渠道。自动同步、手动同步和指定 Key 重扫先核实 Key 的网页身份，再以 `group_id` 读取分组模型及报价。分组规则足够核对时，报价、倍率和分组写入同一轮版本。官网报价须和当前 Key 的分组、计费规则及模型对应，调用按派发时有效的报价和倍率结算。网页读取失败时，教师页保留上次倍率并标记陈旧，后续跟随价待回执核账。新流水约每分钟检查一次。

目录分组的账户倍率以官网账户倍率表 `GET /api/v1/groups/rates` 的直接命中值优先。该表成功读取但缺少该组时，才进入回退。回退来源有两种。共用目录的 `key_billing` 要求该组只有一把活跃 Key，且该 Key 的账单和最近 30 分钟完整用量通过核验。标准分组还可以使用 `group_rate`，它取分组默认倍率，不绑定个人 Key。

`group_rate` 在高峰开启或关闭时都能使用，高峰规则仍参与有效倍率。高峰开启时，高峰倍率、时区和起止时间必须可解析。倍率表整张读取失败时，官网已列分组本轮待核，已核实的历史版本保留。官网目录没有列出该组时，通过该 Key 的账单与型号清单核实。

教师核对 Key 读取网页失败时返回 503，本次已验证的分组或已读完的账日仍可保存。

教师模型页与 Key 卡片共用分组状态。当前组未出现在可用渠道时，同步读取该 Key 的计费和模型清单。明确收到分组权限拒绝时，页面保留 Key 清单中的分组 ID、名称和基础倍率，并显示「账号无 group N 权限」。已匹配的 Key 未绑定分组时显示「Key 分组不存在」，缺少已核实的归属时显示「分组待同步」。Key 的计费和模型清单已核实且缺少报价时显示「官方价表缺失」。可用的流水反推价单独标记。分组诊断需要迁移 `0059`。

当前 Key 分组没有自己的官网价表时，上游价取同一平台公开的官网同名模型基础价，再乘当前 Key 的生效倍率，`0.60` 是测试样例。结算采用有效倍率及对应报价快照，倍率字段和来源见 [API 文档](docs/API_DOCUMENTATION.md)。`key_capabilities[].website_group` 的分组 ID、名称和默认倍率来自分组观察或当前清单；首次成功计费前，官网直读账户倍率可来自该 Key 自己的官网核对快照，要求 30 分钟新鲜度且分组配对；之后 `group_rate` 从匹配的当前倍率版本直接填充，只有官网表直接值和 `key_billing` 账户倍率受 `billing_account_rate` 的修订和成功时间绑定。账户倍率证据的轮次或修订不匹配时只隐藏账户倍率，不单独撤销报价；确认 Key 编号变化、同 Key 换组或无组时，失效旧个人倍率和关联报价。官网表直接值不产生 Key 编号，有效 `key_billing` 证据才带 Key 编号，`group_rate` 不带个人 Key 编号；旧快照失效时以当前 billing 成功轮次写入的官网直接表值优先。历史流水反推价只用于核对和页面参考，不自动扣费。命中反推价的调用保持待核，原因记为 `derived_price_requires_review`，教师确认正式价或保存手动价后才结算。

官网快照保存各渠道、平台、分组、型号和原始计价条件。部分平台读失败时更新可读平台并保留其余数据，整页失败时保留已有快照，页面标记待更新并按退避间隔重试。教师页保存及续期得到的会话加密写入学校数据库，后端重启后会继续读取。

迁移 `0069` 后，同一学生所有 Key 和项目共用北辰待核额度。默认上限为 1000 整积分（`NEBULA_STUDENT_UNRESOLVED_CREDIT_LIMIT`），默认每笔预留 10（`NEBULA_STUDENT_PENDING_RESERVE_CREDITS`）。受理和派发北辰请求时，已有预留加上本笔默认预留达到上限即返回 429。结束时只预留学生剩余的额度。

每名学生默认可同时向上游派发 5 笔调用，所有供应商、Key 和项目共用这 5 个名额。直接运行和本地 Compose 可通过环境变量 `NEBULA_STUDENT_CONCURRENCY_LIMIT` 覆盖默认值。生产 Compose 显式指定 5，服务器 `.env` 中的旧值不会生效。

`NEBULA_UNRESOLVED_EXPOSURE` 默认 `1`。设为 `0` 可整体回退，恢复旧的每学生 20 笔待核准入和 `pending_review_limit`，同时关闭缓存分项试算。仅设 `NEBULA_PENDING_AUTO_SETTLE=0` 时继续按金额限流、按上界预留，暂停推荐分项自动扣款。Gemini 仍待核，不自动结算。阈值、倍率和教师定价说明见 [API 文档](docs/API_DOCUMENTATION.md)。

「模型管理 → 北辰」的主表只显示四把 Key 当前 `GET /v1/models` 探测到的模型；官网目录里的其它分组和仅官网可见的型号单独计数，不进主表。每个模型只有一张价格表：五列（输入、输出、缓存写入 5m、缓存写入 1h、缓存读取）两排（上游基础价、学生实价），不再并列展示其它分组。当前 Key 分组缺完整报价时，用公开官网同名模型的基础价作底价，再乘当前 Key 的生效倍率。目录中保留可直接操作的不同 Key 型号 ID。

「Key 实际能力」显示官网分组名、ID、倍率及型号。点「核对这个 Key」打开弹层，可选填目标倍率，再读取该数字 Key ID 当天和前一天的网页流水。弹层只核对倍率目标。每笔核对账号、Key ID 和北京时间账日，再按日和模型汇总四类 Token、原价与实付，保留金额原单位。账单实付比值等于实付除以原价，分组倍率取官网分组字段。公开接口的账号模型、余额和用量从单 Key 核对与对账中排除。

长期流水读完且核对了归属后，较早的账日也可用于对账。渠道停用后仍可查看已确认的历史账日。每个账日只计入一份上游金额，手工导入可更正金额，完整性仍以官网证据为准。

北辰页面上的 1 个余额单位按 1 元人民币计算，对应 100 个校内基础积分。后台用本次实际的输入、输出和缓存用量分别乘对应单价，应用一次渠道倍率，得到本地基础预估。对这四条学校渠道发出的新调用，学生月度额度实际扣除 **基础积分 × 1.10**。例如老师分配 100 积分，一笔基础预估 10 积分的调用扣 11 积分，学生剩余 89 积分。已获准发出的调用可超出剩余额度，额度用完即停止接受新调用。

北辰官网逐笔流水保留供应商的原始实扣。对账同时列出本地基础预估、校内扣分与北辰实扣。已经按本地预估入账的调用收到流水后核对差额。待核的跟随价调用根据唯一匹配且通过校验的回执实扣补记。对账汇总纳入能确认的 Token，并标记缺项。教师可查询每笔本地调用的 Token、缺项原因和回执差额。学生可凭网关返回的 `X-Request-ID` 查自己的账。[用量与对账接口](docs/API_DOCUMENTATION.md#72-学校级北辰用量与区间对账)

教师进入「供应商管理 → 北辰 → 查看模型」，展开逐笔对账，按北京时间选择账日。每行对应一张已归档的北辰流水，能关联到学校调用时显示学校原来记下的基础积分、当笔倍率、实际扣分和差额。窗口同时显示学校最近一次读到网页价格、最近一次成功读取账单分页的时间，以及已读至末页的覆盖数。尚未取得网页读取授权时，教师仍能到「项目监控」查看已有的校内扣分和待核笔数。

教师在「模型管理 → 北辰」可以一键启用当前筛选结果，也可以对官网缺价或需要覆盖的模型手动定价。模型页统一以积分显示官网标准价和学生实际价，北辰原价以弱化小字保留；每个模型同时显示当天、本月或总计区间内实际消耗的积分与 Token，点开可继续按学生和学校 Key 查看。手动价是学生基础单价，保存后持续覆盖官网，最终扣分仍乘 1.10；调价只影响之后发起的调用，不追溯已经入账或调价前发出的请求。批量启用按当前线路及可计算报价逐项判断，手动价和可计算的纯 Token 条件价都计入报价覆盖。线路就绪后，启用按钮旁和「查看档位」会显示具体的报价问题，例如哪一档缺少输出单价。

当官网报价以 Token 为主时，模型管理和结算使用输入、输出和缓存单价。报价中的按次、图像、视频附带字段保存在原始快照中，忽略的字段名随对应扣分记录保存。空或省略的计费模式默认按 Token 处理，若非零的附带费用使主计费方式无法确认，则提示核对该字段。
北辰模型页按 Key 展开凭据、目录、报价、探活四项状态。后台默认每小时对当前目录里各把 Key 的模型逐协议发送一次最多生成 1 个输出 Token 的短文本请求，覆盖 Chat Completions、Responses、Anthropic Messages 和 Gemini GenerateContent。同一 Key、模型和协议每天只探活一次，全站每日上限由 `BEICHEN_PROBE_DAILY_LIMIT` 控制（默认 500，北京时间自然日）；达到上限后自动和手动探活都会跳过，接口与模型页显示今日已用、上限和剩余。探活会消耗上游用量，输入也可能计费。设置 `BEICHEN_PROBES_ENABLED=false` 关闭自动探活，教师仍可点击「立即探活」提交后台任务。模型页分别显示发布状态和探活结果。已启用模型失去全部北辰可达线路时会标红。在「供应商管理 → 北辰 → 目录差异」可按 Key 查看官网分组与 `/v1/models` 各自多出的模型。

「供应商管理 → 北辰 → 北辰对账」按同一组时间范围比较学校本地 Token 与北辰回传 Token，以及学生已扣折合金额与北辰实扣。100 积分按 1 元换算，差额来自学校 1.10 倍系数、计价差异和账单同步进度；缺少北辰实扣的模型显示未知，不按零计算。页面同时列出缺失账日、待核账笔数、无报价调用和倍率混合提示，并可下钻到每个模型和每条学校渠道 Key。

对账按北京时间账日查询，未关联流水每页最多 50 笔。同一天内可连续翻页，也可切换到「仅完整可比账日」，查看已结束、同步完整且实扣可比的账日。已停用的渠道要求同步至停用当天，后续修改采价配置不会改动这个日期。

「项目管理 → 学生额度」改为顶部汇总卡、左侧分配图和右侧成员列表。每个学生通过独立编辑器调整额度，额度不能低于已用加待核预留，也不能超过项目可分配额度；导师和历史月份只读。弹窗内的月份选择只刷新额度数据，不再联动整页。

校方如果已为指定教师开通独立的远程浏览器应用，可以将固定入口接到上述抽屉。教师在工作区中使用校方授权的浏览器。学校后台实际读取了哪些价格和流水，以页面上的成功时间和归档记录为准。[学校工作区配置](docs/FRP_PUBLIC_ACCESS.md#校方北辰工作区入口)

### 验收北辰官网抓取

在仓库根目录运行下列命令，用构造数据验收。

```powershell
go run ./cmd/beichen-crawl --fixture ./internal/beichen/testdata/website-directory.json
go run ./cmd/beichen-crawl --fixture ./internal/beichen/testdata/website-pricing-2026-11-21.json
```

教师可在供应商页保存北辰账号密码或网页 Token。密码模式可自动续期，也可在北辰拒绝续期后重新登录。网页 Token 模式适合已在北辰浏览器完成验证的会话，到期要手动更新。

在「供应商管理 → 北辰官网 Key 清单」可查看北辰各把 Key 的分组、分组倍率、状态、最近读取时间及对应的学校渠道。后台约每 15 分钟读取北辰登录后的密钥页，也可点「刷新官网 Key」立即核对。分组或倍率变化在当轮写入历史版本，计费仍需要当前分组的有效报价。网页会话过期时保留上次成功读取的清单和时间。

**学校服务器**在可信 HTTPS 教师页的「供应商管理 → 北辰 → 网页登录」保存会话。保存成功后，点「测试抓取」可立即更新学校目录并查看本轮的平台数、模型数和失败项。清除教师在网页保存的登录后，随版本交付的学校网页会话可继续读取官网目录、价格和已核对归属的账单。

**本机**从项目根目录运行 `go run ./cmd/beichen-session login --window`。程序会用默认浏览器打开本机窗口。可输入北辰账号、密码和可选的验证码。遇到需要网页人工验证时，可在北辰网站完成后使用 Token 模式。登录成功后网页显示账号、到期时间和最后验证时间，并可点「测试抓取」读取渠道页或点「刷新会话」立即续期。关闭标签页会停止本机登录服务。

```powershell
go run ./cmd/beichen-session login --window
go run ./cmd/beichen-session status
go run ./cmd/beichen-session check
go run ./cmd/beichen-crawl
go run ./cmd/beichen-crawl --keys
```

Windows 会话保存在 `%LOCALAPPDATA%\Nebula\beichen-session.json`，同目录的 `beichen-session.key` 用于 AES-256-GCM 加密。两个文件只授权给当前 Windows 账号。其他系统在用户配置目录下使用 `Nebula` 和 `0600` 文件权限。「记住账号密码」默认关闭。爬虫优先读取这份会话，到期或渠道页拒绝访问时会尝试续期。北辰未下发续期凭证且密码未保存时，到期后需重新登录。本机测试抓取只读取渠道页和必要的登录接口。

学校的官网渠道、价表、网页 Key 分组与逐笔用量使用教师页保存的会话。现有密封会话、环境凭据及 `beichen-session import` / `export` 仍供已有部署和脚本使用。

需要比对学校现有 Key 时，运行 `go run ./cmd/beichen-crawl --match-db`。学校有多个启用的北辰供应商时，用 `--provider <供应商UUID>` 选定一个。该选项也可与 `--fixture` 一起使用。比对读取已保存的 Key 目录和教师别名。官网读取时，工具可更新已保存的会话状态。

`--keys` 用同一个网页登录会话读取渠道目录和 Key 清单，再用官网提供的各把 Key 请求 `GET https://api.beichenapi.com/v1/models`。每把 Key 显示名称、最多 10 字符的前缀、分组、倍率、高峰规则、官网状态及同组型号，同时列出实际模型数和完整型号，以及 `matched`、`only_key`、`only_group` 三类对照。只有实际接口返回空清单才显示实际型号数为 0。官网仅返回遮盖前缀或模型列表读取失败时，实际型号数为空。此选项无需数据库。`--fixture` 保持离线使用。

需要同步清单到已配置的 PostgreSQL 时使用 `go run ./cmd/beichen-crawl --save-keys`，由 `DATABASE_URL` 和 `PROVIDER_CREDENTIAL_ENCRYPTION_KEY` 选择数据库并匹配学校渠道。存在多个启用的北辰供应商时加 `--provider <供应商UUID>`。学校后台也会约每 15 分钟继续读取官网清单。每把 Key 的分组、倍率或高峰规则变化时记录新版本。`--save-keys` 和教师清单接口都会显示版本首次观察时间 `observed_at` 和最近变化时间 `changed_at`，首次建立基线时最近变化时间为空。

标准输出是 JSON，列出每个型号的来源分组、报价、采集时间、缺项和已忽略字段。`pricing` 保留北辰原始报价，其中 Token 单价以每个 Token 计。`base_token_prices_per_million` 列出每百万 Token 的基础价，金额以北辰余额单位计。脱敏的 2026-11-21 样本包含 35 个不同型号及 9 个分组。部分解析失败仍会输出可用结果和失败项，并以非零状态退出。

后台按约 15 分钟刷新官网目录和分组报价。已核对归属的学校渠道会用各自凭据读取 `GET /v1/models`，按分组对照官网型号，同时记录网页账号可见的个人倍率。教师在「模型管理 → 北辰 → 官网目录 → Key 实际型号核对」查看匹配、仅 Key、仅官网三类及读取失败原因。要立即输出同样的对照，运行 `go run ./cmd/beichen-crawl --compare-models`，使用已配置的数据库、凭据加密密钥和网页授权。重复读取同样的价格和倍率不会增加历史版本。

### 导出北辰仪表盘和 Key 用量

`go run ./cmd/beichen-report --range 7d --granularity day` 读取本机加密会话，输出并保存仪表盘、平台、模型、趋势、最近使用和各把 Key 的用量及核对结果。范围可选 `today`、`7d`、`all`。加 `--reconcile` 后，通过 `DATABASE_URL` 只读比较本机网关账本与本轮网页数据，金额按元记账。[接口、用法和实抓记录](docs/reports/beichen-crawl-stability.md)

`go run ./cmd/pending-audit` 通过只读连接生成北辰待核账报告，包含上游金额区间、推荐缓存拆分和自动结算判定。[参数与判定口径](docs/PENDING_AUDIT.md)

北辰缺终态调用默认按已保存的用量结清。服务启动时扫描一次，之后每 60 秒扫描一次。直接启动和 Compose 均支持以下环境变量，改动后重启服务生效。

| 环境变量 | 默认值 | 作用 |
| --- | --- | --- |
| `NEBULA_TERMINAL_PENDING_POLICY` | `known_only` | 按已知用量结清。设为 `hold` 暂停后台和教师手动处理。无法识别的值同样暂停。 |
| `NEBULA_TERMINAL_PENDING_GRACE_SECONDS` | 300 | 从调用结束算起等待上游补报的秒数，至少 60 秒。 |
| `NEBULA_TERMINAL_PENDING_BATCH` | 100 | 每次最多处理的调用数，范围 1 至 500。 |

超过宽限期后，未知输出按 0。没有可计费用量或无法可靠定价时按 0 积分核销，并记录原因。教师可在供应商对账页点击「只读回放」，查看缺终态总数并确认立即处理一批。此策略可能少收，以确保未知用量绝不多扣。

缓存或计数证据互相矛盾的调用不会被缺终态策略按 0 核销，而是保留在人工待核队列，教师端会单独提示原因。

教师可在供应商的北辰对账页每批回放 200 笔待核调用，使用「查看下一批」翻页。缺少派发价格的调用计入总数并保留待核。教师核对预计扣分后，在弹窗内确认本批可自动项的真实扣款。有金额上界但仍需待核的调用按学生剩余额度更新预留。待核集合变化后需重新回放。

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
