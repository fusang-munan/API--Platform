# 北辰仪表盘、Key 与本地账本抓取

报表读取北辰普通账号的网页接口。复核工作另用各把 Key 请求模型、用量和计费接口。所有远端请求使用 HTTPS GET。会话失效时结束当前命令，登录和续期仍由本机登录工具管理。报表只保存下文列出的统计值和 Key 元数据。

## 运行命令

从仓库根目录运行。缺省范围为近 7 天。命令将同一份 JSON 打到标准输出，并保存到 `tmp/beichen-report-*.json`。用 `--out` 可指定新的文件路径。

```powershell
go run ./cmd/beichen-report --range today --granularity day
go run ./cmd/beichen-report --range 7d --granularity day
go run ./cmd/beichen-report --range all --granularity day
go run ./cmd/beichen-report --range all --granularity day --reconcile
```

`--reconcile` 从 `DATABASE_URL` 读取本机 PostgreSQL 的连接信息。学校环境包含多个北辰供应商时，在命令末尾加 `--provider <供应商UUID>`。程序使用只读事务查询，统一按北京时间日期对齐。金额按学校记账约定以元（`CNY`）原数值记录。网页截图里的 `$` 对比列保留其显示样式。学生扣分以每 100 积分折合 1 元计算。

网页统计在不同请求之间可能变化。`started_at` 与 `captured_at` 记录网页读取的时间段。`local_read_at` 记录本地查询的开始时间。三个时间都带时区。成功抓取后，即使出现用量差异，命令也保存完整报表。查看 `key_reconciliation.matches`、`key_reconciliation.inventory_matches_dashboard` 和 `local_reconciliation` 了解对账结果。数量差额使用整数，金额差额使用十进制计算，绝对值在 `0.000000000001` 元以内按零处理。

## 接口稳定性清单

`stable` 表示本次通过实际响应和字段验证，可复用当前契约。`experimental` 表示观察到限制或需要额外验证，只用于注明的用途。`unsupported` 表示已实测的接口无法提供该项数据。`blocked` 表示当前任务的授权或验证条件未满足。接口版本由北辰维护，表中时间为 2026-11-21 北京时间。

| 页面或用途 | 方法和路径 | 参数 | 返回字段 | 稳定性 | 最近验证 | 备注 |
| --- | --- | --- | --- | --- | --- | --- |
| 仪表盘入口 | GET `/dashboard` 和 `/assets/*.js` | 无 | 页面及分包文件名 | stable | 19:36 | 依据网页程序中的实际请求和字段读取方式查找接口。分包文件名随北辰版本变动。 |
| 账号余额 | GET `/api/v1/auth/me` | `timezone=Asia/Shanghai` | `data.id`、`data.balance`、`data.created_at` | stable | 19:52 | 余额是读取当刻的可用金额，注册日用于 `all` 的日期起点。 |
| 仪表盘汇总 | GET `/api/v1/usage/dashboard/stats` | `timezone=Asia/Shanghai` | `today_*` 和 `total_*` 的请求、总 Token、输入、输出、缓存读取、缓存创建、标准价和实扣，`total_api_keys`、`active_api_keys` | stable | 19:52 | 网页顶部始终展示今日和累计。传入今天的日期后仍返回这两组统计。 |
| 仪表盘平台卡片 | 上一接口的 `data.by_platform` | 同上 | `platform`、`today_requests`、`today_tokens`、`today_actual_cost`、对应三个 `total_*` 字段 | experimental | 19:52 | 可复现原网页卡片。网页把累计请求和 Token 放在今日消费旁边。两平台的请求数合计比账号少 6，Token 与金额合计一致。 |
| 自选范围统计和单 Key 汇总 | GET `/api/v1/usage/stats` | `start_date`、`end_date`、`timezone`，可选 `api_key_id` | `total_requests`、`total_tokens`、`total_input_tokens`、`total_output_tokens`、`total_cache_read_tokens`、`total_cache_creation_tokens`、`total_cost`、`total_actual_cost` | stable | 19:52 | 结束日期含当日。账号与 4 把 Key 均已实抓，三个范围的数量和金额均对上。传 `api_key_id` 可稳定取单 Key 汇总。 |
| 模型分布 | GET `/api/v1/usage/dashboard/models` | `start_date`、`end_date`、`timezone`，可选 `api_key_id` | `data.models[]` 的 `model`、`requests`、四类及总 Token、`cost`、`actual_cost` | stable | 19:52 | 账号和 4 把 Key 共用，已核对每把 Key 的模型合计。空范围返回 `models: []`。 |
| 按天趋势 | GET `/api/v1/usage/dashboard/trend` | `start_date`、`end_date`、`timezone`、`granularity=day` | `data.trend[]` 的 `date`、`requests`、四类及总 Token、`cost`、`actual_cost` | stable | 19:52 | 只有有记录的日期。仅传 `range=today` 实测仍返回默认日期范围。 |
| 最近使用及逐笔流水 | GET `/api/v1/usage` | `start_date`、`end_date`、`timezone`、`page`、`page_size`、`sort_by`、`sort_order`，可选 `api_key_id` | `data.items[]`、`page`、`page_size`、`pages`、`total`。用量行有 `id`、`user_id`、`api_key_id`、`created_at`、`model`、四类 Token、`total_cost`、`actual_cost`、可选 `group.platform` | stable | 19:52 | 最近使用取 `created_at desc` 的前五条，卡片 Token 为输入加输出。逐笔流水用 `id asc` 和每页 100 条，检查条数、顺序和归属。真实账号本轮 53 条，跨页用脱敏样例验证。 |
| 近 7 天按流水分组平台 | 上一接口的 `items[].group.platform` | 同逐笔流水 | 请求数、四类 Token 合计、实扣 | experimental | 19:47 | 这组统计解释 Key 分组归属下的近 7 天平台用量。实测 Claude 12、OpenAI 41 条，合计 53。缺分组平台时列入 `unattributed`。 |
| Key 清单 | GET `/api/v1/keys` | `page`、`page_size=100` | `data.items[]` 的 `id`、`user_id`、`name`、`status`、`group_id`、`group.name`、`group.rate_multiplier` 和分页信息 | stable | 19:52 | `/keys` 稳定。只保存 Key 数字 ID、名称、分组和状态。历史用量按 Key 数字 ID 归属，分组和倍率是清单读取时的状态。 |
| 单 Key 实际模型目录 | GET `https://api.beichenapi.com/v1/models` | 用对应 Key 的 Bearer 鉴权 | `data[].id` | experimental | 20:11 实抓 | 四把 Key 均走 `api.` 子域。`cc不满血` 返回 2 个型号，国模返回 3 个，gpt 返回 5 个。哈吉米为 HTTP 403 `GROUP_NOT_ALLOWED`，型号数未知。 |
| 官网 Key 详情 | GET `/api/v1/keys/{id}` | 网页会话，使用清单中的数字 Key ID | `data.group_id`、`data.status`、`data.group`、`data.ip_whitelist`、`data.ip_blacklist` | stable | 20:11、20:14 实抓 | 哈吉米 Key 仍为 `active`，group 5 的 `is_exclusive=true`，Key 的 IP 白名单和黑名单均为 `null`。Group 16 详情确认分组和 0.60×，未提供两款 Claude 的 Token 单价。 |
| 渠道列表与弹层价格 | GET `/api/v1/channels/available` | 网页会话，可选 `group_id` | `data.platforms[].groups[]`、`data.platforms[].supported_models[].pricing` | experimental（待 R 确认） | 20:11、20:14 实抓 | 可稳定取渠道列表和弹层价格。R 复核前暂按 `experimental` 记录，确认后更新为 `stable`。 |
| Group 16 的渠道报价 | GET `/api/v1/channels/available?group_id=16` | 网页会话 | 渠道、分组、型号和报价结构 | unsupported | 20:11、20:14 实抓 | HTTP 200。20:14 与无查询参数的响应逐字节相同，均为 35 个型号和 9 个分组，缺 group 16。列表与弹层价格可用不等于该组正式 Token 单价可用。 |
| Group 16 的分组详情 | GET `/api/v1/groups/16` | 网页会话 | 纯文本 404 | unsupported | 20:11 实抓 | 当前账号无法从此路径取得分组或报价详情。 |
| 当前账号的分组倍率覆盖 | GET `/api/v1/groups/rates` | 网页会话 | `data` 中由分组 ID 到倍率的映射 | stable | 20:11 实抓 | group 6 为 0.08×，group 10 为 0.20×。 |
| 单 Key 实时计费规则 | GET `https://api.beichenapi.com/v1/sub2api/billing` | 用对应 Key 的 Bearer 鉴权 | `billing_scope`、分组倍率、账户覆盖、已选倍率、生效倍率、高峰状态 | experimental | 20:11 实抓 | 成功响应为 `billing_scope=token`。CC2 为 0.60×、GM1 为 0.08×、gpt 为 0.20×。CC2 响应未提供 `pricing` 或两款模型的单价。哈吉米返回 `GROUP_NOT_ALLOWED`。 |
| 用量页的合并图表 | GET `/api/v1/usage/dashboard/snapshot-v2` | 日期、时区、`granularity=day`、`include_trend`、`include_model_stats`、`include_group_stats` | `trend`、`models`、`groups` | experimental | 19:36–19:39 | 分组汇总实测有 5 行，历史分组仍可能出现在流水中。当前报表使用单项接口。 |
| Key 页的批量消费接口 | POST `/api/v1/usage/dashboard/api-keys-usage` | `api_key_ids` | 前端读取 `stats` | blocked | 19:36 静态核对 | 本次授权限 GET，单 Key 已走 GET 汇总和模型接口。 |
| Key 每日端点 | GET `/api/v1/user/api-keys/{id}/usage/daily` | `days` | 已定位网页客户端定义 | experimental | 19:36 静态核对 | 本次日、模型、Key 组合使用按页读取的用量流水。每日端点的返回契约待实抓验证。 |
| 公开用量端点 | GET `https://api.beichenapi.com/v1/usage` | 分别用四把 Key 鉴权，传日期区间、`days=7` 和 `timezone=Asia/Shanghai` | `daily_usage[].cost`、`actual_cost`、`requests`，另有 `model_stats` 和 `unit` | experimental | 20:11 实抓 | 账号级汇总。三把 Key 的返回汇总各自和按数字 Key ID 校验过的官网逐笔流水一致。哈吉米返回 HTTP 403 `GROUP_NOT_ALLOWED`。成功响应没有返回数字 Key ID，单 Key 归属以逐笔核验或 `/api/v1/usage/stats?api_key_id=` 为准。 |
| 本地网关账本 | PostgreSQL 只读查询 `gateway_calls`、已核实回执与发件时的价格版本 | 北辰供应商、起止日 | 日期、模型、Key、请求、四类 Token、本地金额和缺项 | stable | 19:52 | 隔离 PostgreSQL 已验证同组匹配、单侧记录、待核账、断档和元转换。 |
| 实际学校账本与官网覆盖率 | 本地对账命令 | 业务库的本机连接及北辰供应商 | `local_reconciliation` | blocked | 19:52 | 当前环境没有提供实际业务库连接。学校实际覆盖率待验证。 |

### 2026-11-21 补充确认

- `GET /api/v1/channels/available` 可稳定取渠道列表和弹层价格，返回 `data.platforms[].groups[]` 与 `data.platforms[].supported_models[].pricing`。本轮先记为 `experimental（待 R 确认）`，待 R 复核后更新为 `stable`。
- `GET /api/v1/keys` 稳定。分页、Key 数字 ID、名称、状态、分组和倍率字段沿用当前契约。
- `GET https://api.beichenapi.com/v1/usage` 是账号级汇总，不返回数字 Key ID；单 Key 归属仍以网页逐笔流水或下一项的单 Key 汇总为准。
- `GET /api/v1/usage/stats` 传 `api_key_id` 可稳定取得单 Key 汇总，账号级与单 Key 的请求、四类 Token、总 Token、标准价和实扣均已实抓核对。

网页的今日和近 7 天预设都会生成 `start_date`/`end_date`。本命令把 `today` 映射为今日，`7d` 映射为今日及前 6 天，`all` 从账号注册日查到今日。总计结果也与仪表盘累计值核对。网页日期选择器的预设清单中无 `all` 字段。[仪表盘网页分包](https://beichenapi.com/assets/DashboardView-D1joLxp7.js)、[用量请求分包](https://beichenapi.com/assets/usage-DjjR1LYa.js)、[日期选择器分包](https://beichenapi.com/assets/TokenUsageTrend.vue_vue_type_script_setup_true_lang-BClGdzAe.js)

## 与截图数据对比

同一账号于 **2026-11-21 19:47:17 至 19:47:33（北京时间）** 抓取三份报表。近 7 天为 11 月 15 日至 21 日，累计为 11 月 18 日至 21 日。比较沿用截图中表示金额的 `$` 样式。

| 项目 | 截图给定值 | 接口值 | 结果 |
| --- | --- | --- | --- |
| 今日请求、实扣 | 39 次 / $0.0098 | 39 次 / 0.009815584 元 | 一致 |
| 累计请求、实扣 / 标准价 | 53 次 / $0.0263 / $0.1086 | 53 次 / 0.0262810015 / 0.108563125 元 | 一致 |
| 今日 Token | 79.7K，输入 33.7K，输出 665，缓存 45.3K | 79,684，输入 33,707，输出 665，缓存读 45,312，缓存写 0 | 一致 |
| 累计 Token | 157.0K，输入 103.0K，输出 1.3K，缓存 52.7K | 156,993，输入 103,044，输出 1,290，缓存读 52,659，缓存写 0 | 一致 |
| 可用余额 | $4.97 | 4.97371898 元 | 一致 |
| Key 数 | 4 | 4，把数与清单一致且均为活跃 | 一致 |
| Claude 卡片 | 12 次 / 276 Token / $0.0007 今日实扣 | 累计 12 次 / 276 Token，今日实扣 0.0006696 元 | 一致 |
| OpenAI 卡片 | 35 次 / 156.7K Token / $0.0091 今日实扣 | 累计 35 次 / 156,717 Token，今日实扣 0.009145984 元 | 一致。接口另给今日 21 次、79,408 Token |
| gemini-3.8-flash | 9 次 / 73.0K | 9 次 / 72,953 | 一致 |
| glm-5.3-flash | 11 次 / 32.5K | 11 次 / 32,498 | 一致 |
| gpt-6.1-sol | 3 次 / 12.7K | 3 次 / 12,692 | 一致 |
| gpt-6.2-sol | 2 次 / 10.9K | 2 次 / 10,858 | 一致 |
| gpt-6.2-astra | 2 次 / 10.8K | 2 次 / 10,756 | 一致 |
| 按天趋势 | 11-18 至 11-21 | 11-18 有 5 次，11-19 无流水，11-20 有 9 次，11-21 有 39 次 | 日期范围一致 |
| 最近一条 | claude-sonnet-5-5，23 Token，$0.0000 / $0.0001 | 11-21 16:55:42，23 Token，实扣 0.0000372 / 标准价 0.000062 元 | 一致 |

平台卡片累计为 47 次，账号累计为 53 次。网页逐笔流水中，OpenAI 分组共 41 次，比 OpenAI 卡片多 6 次。这个累计差额与国模 Key 的 6 次流水数相同。OpenAI 卡片与对应分组的 Token 和金额也分别相同。平台卡片只提供汇总字段，具体少计了哪些调用仍待北辰确认。

## 四把 Key 校验

金额列单位为元。Key 分组名及倍率来自清单，实际费用来自用量接口。

| Key | 今日请求 | 累计请求 | 累计输入 | 累计输出 | 累计缓存读 | 累计缓存写 | 累计总 Token | 累计标准价 | 累计实扣 | 累计模型数 |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| gpt（12090） | 10 | 11 | 1,435 | 57 | 49,408 | 0 | 50,900 | 0.0227664 | 0.00455328 | 5 |
| 国模（12093） | 4 | 6 | 228 | 26 | 0 | 0 | 254 | 0.001178 | 0.00009424 | 2 |
| cc不满血（12094） | 25 | 25 | 32,265 | 609 | 0 | 0 | 32,874 | 0.0303084 | 0.005340384 | 4 |
| 哈吉米（12098） | 0 | 11 | 69,116 | 598 | 3,251 | 0 | 72,965 | 0.054310325 | 0.0162930975 | 2 |
| 合计 | 39 | 53 | 103,044 | 1,290 | 52,659 | 0 | 156,993 | 0.108563125 | 0.0262810015 |  |

今日账号与四 Key 合计均为 39 次、79,684 Token、标准价 0.0531752 元、实扣 0.009815584 元。近 7 天的合计与累计相同。三个范围里，所有请求、四类 Token、总 Token、标准价及实扣的差值均为 0。每把 Key 的模型合计也与对应汇总一致。

### 20:11–20:14 复核模型目录、CC2 报价与实际扣费

**哈吉米的 403 来自专属分组授权。** 四把 Key 都在同一台机器请求 `https://api.beichenapi.com/v1/models`。哈吉米的模型、用量和实时计费接口均返回 `GROUP_NOT_ALLOWED`，错误文案为「API Key 所属专属分组不再允许当前用户使用」。网页 Key 详情显示 `active`、group 5 为专属分组，且两个 IP 清单字段均为空。其余三把 Key 在同一 API 子域返回 HTTP 200。

**Group 16 当前的官方 Token 单价查询为 `unsupported`。** 模型目录确认有 `claude-opus-5-5` 和 `claude-sonnet-5-5`。渠道目录带 `group_id=16` 的响应和不带参数的响应逐字节一致。`GET /api/v1/keys/12094` 提供分组元数据，`GET /api/v1/groups/16` 返回 404。该 Key 的 `/v1/sub2api/billing` 确认实时倍率为 0.60×，高峰关闭，响应没有模型价表。

已校验的 group 16 流水仍能复原这两款模型的历史输入和输出计价。分别用流水的 `input_cost / input_tokens` 和 `output_cost / output_tokens` 折算到每百万 Token，再乘同笔实际倍率。下表按北辰账单原数值记录。这 12 笔的 `currency` 字段为空，缓存项缺少可核实单价。

| Group 16 型号 | 流水数 | 观测输入／输出原价（每百万 Token） | 观测输入／输出实扣单价（每百万 Token） | 流水原价合计 | 流水实扣合计 |
| --- | ---: | --- | --- | ---: | ---: |
| `claude-opus-5-5` | 6 | 4 / 20 | 2.4 / 12 | 0.000744 | 0.0004464 |
| `claude-sonnet-5-5` | 6 | 2 / 10 | 1.2 / 6 | 0.000372 | 0.0002232 |

| Group 16 报价路径 | 稳定性 | 使用范围 |
| --- | --- | --- |
| 官网正式价 | `unsupported` | 官网渠道页当轮未返回 group 16 的模型价格。后续同步读到同组正式价时优先采用。 |
| 历史流水反推价 | `derived_from_usage` · 反推价，待核对 | 各 6 笔流水支持上表的基础输入、输出价。使用时须核对当前 Key、分组、实际型号及倍率。缓存单价缺证据。 |
| 教师手动修正 | `teacher_manual_price` | 教师可按 Key 保存校内基础价。修改自下一笔派发起生效，历史流水保留所用价格版本。 |

分组报价历史保留 20:11–20:14 复核窗口末尾的观察时间，使用 0.60× 时可复算上表实扣单价。20:38 的只读复核再次确认 group 16 Key 返回两款型号、倍率为 0.60×，且官网渠道页仍无该组报价。教师界面和账本报告展示反推来源。混有不同报价来源的账日行标记「含反推价，待核对」。

**GM1 当前实际生效 0.08×。** 分组默认值是 0.10×。`/api/v1/groups/rates` 给当前账号的 group 6 设置了 0.08×，该 Key 的 `/v1/sub2api/billing` 同时返回 `user_rate_multiplier=0.08`、`resolved_rate_multiplier=0.08` 和 `effective_rate_multiplier=0.08`。国模的 6 笔 group 6 流水均记录 0.08，且通过 `0.00009424 ÷ 0.001178 = 0.08` 复算。gpt 也有账户覆盖，默认 0.18×，生效 0.20×。

用量复核覆盖北京时间 11 月 15 日至 21 日，最后一天取采样时已发生的流水。观测倍率为 `Σ actual_cost ÷ Σ cost`。网页流水在核对账号、数字 Key ID、日期和分页完整性后汇总，原价缺少 `cost` 时读取 `total_cost`。三把成功读取 `/v1/usage` 的 Key，其计数、原价、实扣与各自网页流水一致。公开接口的 `unit` 原样标为 `USD`，下表按接口原数值计算比例。

| Key | `/v1/usage` | 已核对流水 | 原价合计 | 实扣合计 | 区间观测倍率 | 当前实时倍率 |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| 哈吉米 | HTTP 403 | 11（官网流水） | 0.054310325 | 0.0162930975 | 0.30×（历史） | 未确认，专属分组已拒绝 |
| cc不满血 | HTTP 200 | 25 | 0.0303084 | 0.005340384 | 约 0.176201×（含两个分组） | 0.60× |
| 国模 | HTTP 200 | 6 | 0.001178 | 0.00009424 | 0.08× | 0.08× |
| gpt | HTTP 200 | 11 | 0.0227664 | 0.00455328 | 0.20× | 0.20× |

`cc不满血` 区间内有 group 15 的 13 笔按 0.16× 扣费，还有 group 16 的 12 笔按 0.60× 扣费。约 0.176201× 是两组收费的加权结果。

## 本地对账字段和覆盖率

程序先把本次网页逐笔流水按「北京时间日期 + 官网模型名 + 官网数字 Key ID」汇总，再与本地按同一组字段查询的结果合并。本地使用发件时锁定的 Key ID 和模型，Token 按网关已有协议统计规则归一。`website_ledger_check` 先核对逐笔合计与网页的区间统计，供检查拉取是否完整。

| 输出 | 读取与计算口径 |
| --- | --- |
| `website` | 本轮 GET 流水的请求数、四类及总 Token、标准价 `total_cost` 与实扣 `actual_cost`。 |
| `local` | 本地 `gateway_calls` 中已进入网关统计范围的请求和 Token。`standard_cny` 是按发件时锁定的普通 Token 基础报价算出的估计，来源写入 `standard_source`。缺失报价、分档费率、特殊计价或缓存写入档位缺证据时显示 `null`。`actual_cny` 只汇总无冲突、与本地调用及 Key 匹配的已核实回执实扣。 |
| `student_credits`、`student_cny` | 网关记录的已扣学生积分及其折合金额。部分请求尚未扣分时提供已扣小计和 `student_charged_calls`。 |
| `website_minus_local` | 同组的网页值减本地值。只有一侧有记录时仍保留请求数差额，Token 或金额证据缺失时相应差额为 `null`。 |
| `student_minus_website_actual_cny` | 学生已扣折合金额减去网页实扣。全组学生扣分和网页实扣齐全时才有值。学校倍率、上限、手动报价等都会影响此差额。 |
| `coverage` | 两侧分别计算「双方都有的日期、模型、Key 行数 ÷ 本侧行数」，并给出「有效回执本地请求数 ÷ 本地请求数」。分母为零时为 `null`。同组出现表示统计行可以比较，逐笔相等还需查看差值和回执覆盖率。 |
| `missing_reasons` | 列出哪侧缺行、官网 Key ID 未锁定、Key 已不在当前清单、Token 缺项、标准价无法估算、回执缺失或冲突、待核账或学生扣分未齐。缺行仅说明该组在本次范围内缺记录。 |

**对账实跑。** 19:52:19 前抓到北辰 53 条，上卷为 15 个日期、模型、Key 组合，逐笔合计与账号完全一致。19:52:20 读入隔离的空白 PostgreSQL，15 行均标为 `website_only`，网页侧行覆盖率为 0%，本地侧因无记录为 `null`。另用脱敏本地数据验证 1 行双方匹配、1 行仅网页、1 行仅本地且 Key 无法归属，双侧行覆盖率和本地回执覆盖率均为 50%。实际业务账本未参与本轮验收。

## 测试结果

2026-11-21 19:59 完成 `go build ./...`、`go vet ./...` 和连接隔离 PostgreSQL 17.12 的 `go test -count=1 ./...`，均通过。报表测试覆盖三个范围、按天粒度、单 Key 过滤、两页流水、空数据、HTTP 401 和正文 401、金额差额及本地逐行对账。

## 抓取失败时

- 返回 `HTTP 401` 或提示会话需要重新登录时，用现有本机登录流程更新会话，再运行报表。报表抓取过程保持原加密会话文件内容不变。
- 逐笔翻页总数或顺序有变化时，命令会报错。重新抓取可获得同一时间段的新快照。
- 仅按范围读网页时无须配置 PostgreSQL。对账需要本机数据库已应用本分支的迁移。连接错误和缺少实际账本记录须分别排查。
