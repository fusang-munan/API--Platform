# Nebula 首期 API 契约

## 1. 文档范围与状态

额度设置以千分之一积分为单位。从 `0.10.0` 开始，北辰用量和调用金额返回精确十进制字符串，可能超过三位小数。待核账调用的状态值为 `pending_review`，计入已扣金额的部分为零。usage/call/input DTO 使用 snake_case。学生申请必填 `requested_monthly_credits`。审批可沿用已有成员额度，首次分配额度时需提供正数 `monthly_credits`。

导师项目用量响应同时返回顶层 `free_models` 项目汇总和 `members[].free_models` 成员明细；两者的每个模型均包含 `id`、`name`、固定 `credits: "0.000"` 与 `calls`。同一模型的项目 `calls` 必须等于全部成员对应 `calls` 之和。

`members[].keys[]` 同时返回 `models` 白名单摘要（`id`、`name`），供调用日志和输入监控按“项目 → 成员 → Key → 模型”进行级联筛选；下级为空表示当前上级范围内全部。

本文档定义首期控制面 `/api/v1` 与模型网关 `/v1` 的已实现契约。Gin Handler、控制面 Service、Redis SSE、审批事务和网关运行逻辑均已落盘；实际运行依赖已初始化的 PostgreSQL、Redis 和有效环境配置。

契约依据参考项目真实 Controller、Schema 与 Gateway 路由重新设计，不兼容旧 `/api/nebula/gateway`、无 `/v1` 路径别名或 internal usage/monitor 路由。

## 2. 通用约定

### 2.0 运维健康检查

| 方法 | 路由 | 鉴权 | 成功 | 用途 |
| --- | --- | --- | --- | --- |
| GET | `/health/live` | 无 | `200` | 进程存活探针，不访问依赖 |
| GET | `/health/ready` | 无 | `200` | PostgreSQL 与 Redis 均可用时就绪 |

`/health/ready` 依赖不可用时返回 `503`。健康响应不使用控制面 envelope，且只暴露 `ok/unavailable`，不得返回 DSN、host、账号、异常栈或其他内部连接细节。

### 2.1 传输与格式

- 控制面请求和响应使用 HTTPS + UTF-8 JSON；SSE 除外。
- 控制面时间字段使用 RFC 3339 UTC，例如 `2026-08-18T09:30:00Z`。
- 资源 ID 为 UUID 字符串；模型的对外 `model_id` 为字符串。
- 未知 JSON 字段应拒绝并返回 `VALIDATION_ERROR`。
- 首期列表按文档指定的稳定字段排序并全量返回，`meta.next_cursor` 固定为 `null`；引入分页时必须同时更新 Handler、Service、本文档和测试。

### 2.2 控制面鉴权

- 受保护接口使用 `Authorization: Bearer <access_token>`。
- Access token 为短期 JWT；Refresh token 为不透明随机值，只通过 `Secure; HttpOnly; SameSite=Lax` Cookie 传输。
- Refresh token 每次刷新都轮换；Redis 中保存 token hash、session family 和 TTL。复用已轮换 token 时撤销整个 family。
- 禁用用户、退出登录、重置密码后，其相关 refresh session 必须撤销。
- RBAC 以服务端 JWT 身份和数据库中的最新用户状态为准，客户端不得提交或覆盖角色。

### 2.3 成功响应

控制面统一使用 envelope：

```json
{
  "data": {},
  "meta": {
    "next_cursor": null
  },
  "request_id": "01K2..."
}
```

单资源响应可省略 `meta`。创建成功使用 `201 Created`；无响应体的成功操作使用 `204 No Content`。

### 2.4 错误响应

```json
{
  "error": {
    "code": "VALIDATION_ERROR",
    "message": "request validation failed",
    "details": [
      {"field": "email", "reason": "invalid_format"}
    ]
  },
  "request_id": "01K2..."
}
```

| HTTP | 错误码 | 场景 |
| --- | --- | --- |
| 400 | `VALIDATION_ERROR`, `REJECTION_REASON_REQUIRED` | JSON、字段或业务输入不合法；驳回申请必须填写原因，通过意见可以省略 |
| 401 | `AUTHENTICATION_REQUIRED`, `INVALID_CREDENTIALS`, `TOKEN_EXPIRED` | 未登录或凭据失效 |
| 403 | `FORBIDDEN`, `ACCOUNT_DISABLED` | 角色或资源范围不允许 |
| 404 | `RESOURCE_NOT_FOUND` | 资源不存在或调用者不可见 |
| 409 | `EMAIL_ALREADY_REGISTERED`, `RESOURCE_NAME_CONFLICT`, `INVALID_STATE_TRANSITION`, `PROJECT_HAS_NO_MENTOR`, `MENTOR_NOT_IN_ORGANIZATION`, `MENTOR_ALREADY_PROJECT_MEMBER`, `MODEL_NOT_READY`, `KEY_ALREADY_CLAIMED` | 唯一性或状态冲突；老师审批导师项目申请时会区分导师未加入组织和已加入项目 |
| 422 | `VERIFICATION_CODE_INVALID`, `VERIFICATION_CODE_EXPIRED`, `INVITATION_INVALID` | 可解析但无法完成认证流程 |
| 429 | `RATE_LIMITED`, `VERIFICATION_LOCKED` | 冷却或失败锁定 |
| 500 | `INTERNAL_ERROR` | 未预期服务端错误，不泄露内部细节 |
| 503 | `DEPENDENCY_UNAVAILABLE` | PostgreSQL、Redis 或邮件服务不可用 |

### 2.5 核心资源形状

```json
{
  "user": {
    "id": "uuid",
    "name": "Alice",
    "email": "alice@example.com",
    "role": "student",
    "status": "active",
    "created_at": "2026-08-18T09:30:00Z"
  },
  "organization": {
    "id": "uuid",
    "name": "AI Lab",
    "description": null,
    "status": "active"
  },
  "project": {
    "id": "uuid",
    "organization_id": "uuid",
    "name": "Research Assistant",
    "description": null,
    "status": "active",
    "has_mentor": true
  },
  "model": {
    "id": "uuid",
    "model_id": "gpt-4.1-mini",
    "display_name": "GPT-4.1 mini",
    "description": null,
    "category": "text",
    "capabilities": ["chat", "tools"],
    "input_modalities": ["text", "image"],
    "output_modalities": ["text"],
    "context_window": 1048576,
    "max_output_tokens": 32768,
    "is_common": true,
    "status": "active"
  }
}
```

## 3. 认证与当前用户

| 方法 | 路由 | 鉴权 | 用途 |
| --- | --- | --- | --- |
| POST | `/api/v1/auth/verification-codes` | 无 | 发送学生注册验证码 |
| POST | `/api/v1/auth/register/student` | 无 | 学生注册（仅创建 student profile） |
| POST | `/api/v1/auth/login` | 无 | 校验账号密码，返回可选身份列表并写入 login challenge cookie |
| POST | `/api/v1/auth/session` | Login Challenge Cookie | 选择 `student` / `mentor` / `teacher` 身份并签发 JWT + refresh cookie |
| POST | `/api/v1/auth/refresh` | Refresh Cookie | 轮换 refresh token 并签发 access token（保留 session 身份） |
| POST | `/api/v1/auth/logout` | Refresh Cookie，可带 Access token | 撤销当前 refresh session 并清除 Cookie |
| POST | `/api/v1/auth/password/forgot` | 无 | 向已注册邮箱发送重置验证码；始终返回同一结果以避免枚举用户 |
| POST | `/api/v1/auth/password/reset` | 无 | 使用验证码重置密码并撤销全部 refresh session |
| POST | `/api/v1/auth/teacher-invitations/activate` | 无 | 受邀老师设置姓名和密码并激活账户 |
| GET | `/api/v1/me` | 任意登录用户 | 获取当前用户、session 身份与全部可用身份 |

### 3.1 请求与响应

发送注册验证码：

```json
{"email":"alice@example.com","purpose":"student_registration"}
```

`purpose` 只能为 `student_registration`。成功返回 `202 Accepted`，响应不包含验证码。

学生注册请求：

```json
{
  "name": "Alice",
  "email": "alice@example.com",
  "password": "user-provided-password",
  "verification_code": "123456"
}
```

注册成功返回 `201 Created` 和 `user`（`role=student`），并创建 `student_profiles` 行。导师身份只能由老师后续授予。

部署可通过运行时 Secret 配置固定学生/导师测试账号。bootstrap 会为 mentor 测试账号创建 `users.role=student` 及 student + mentor profile；已有同邮箱用户不会被 Secret 覆盖。

登录第一步返回可选身份列表（不签发 JWT）：

```json
{
  "data": {
    "user": {"id":"uuid","name":"Alice","email":"alice@example.com","role":"student","status":"active"},
    "identities": ["student", "mentor"]
  }
}
```

同时写入 httpOnly `nebula_login_challenge` cookie。第二步选择身份：

```json
{"identity":"mentor"}
```

成功后清除 challenge cookie，设置 refresh cookie，并返回 session：

```json
{
  "data": {
    "access_token": "jwt",
    "token_type": "Bearer",
    "expires_in": 900,
    "identity": "mentor",
    "user": {"id":"uuid","name":"Alice","email":"alice@example.com","role":"mentor","status":"active"}
  }
}
```

`user.role` 与 JWT `role` claim 均为当前 session 身份，不是 `users.role` 列（非 teacher 账号该列恒为 `student`）。

`/me` 返回：

```json
{
  "data": {
    "user": {"id":"uuid","name":"Alice","email":"alice@example.com","role":"mentor","status":"active"},
    "identity": "mentor",
    "identities": ["student", "mentor"]
  }
}
```

统一登录请求：

```json
{"email":"alice@example.com","password":"user-provided-password"}
```

登录和 refresh 成功均设置新的 Refresh Cookie，并返回带 `identity` 的 session（见上）。

忘记密码请求只包含 `email`。重置请求包含 `email`、`verification_code`、`new_password`。老师邀请激活请求包含 `token`、`name`、`password`。

## 4. 实时事件

| 方法 | 路由 | 鉴权 | Content-Type |
| --- | --- | --- | --- |
| GET | `/api/v1/events` | 任意登录用户 | `text/event-stream` |

客户端使用支持自定义 Header 的 fetch streaming 方式携带 Bearer token。服务端发送心跳注释，并支持标准 `Last-Event-ID`。事件只用于提示刷新，不作为权威状态。

```text
id: 01K2...
event: api_key.status_changed
data: {"api_key_id":"uuid","status":"pending_teacher","updated_at":"2026-08-18T09:30:00Z"}

id: 01K2...
event: models.common_changed
data: {"revision":"01K2..."}
```

`api_key.status_changed` 仅发给该 Key 的学生；`models.common_changed` 发给全部已认证用户。断线重连、事件过期或 revision 变化后，客户端必须重新调用对应 REST 列表接口。

## 5. 学生接口

以下接口要求 `role=student` 且用户为 `active`。

| 方法 | 路由 | 用途 |
| --- | --- | --- |
| GET | `/api/v1/student/organizations` | 列出平台全部 ACTIVE 组织 |
| GET | `/api/v1/student/organizations/{organization_id}/projects` | 列出该 ACTIVE 组织全部项目，并返回真实状态与 `has_mentor` |
| GET | `/api/v1/student/models` | 模型广场 |
| POST | `/api/v1/student/api-keys` | 提交 Key 申请 |
| GET | `/api/v1/student/api-keys` | 列出自己的申请和 Key |
| GET | `/api/v1/student/api-keys/{api_key_id}` | 查看自己的审核详情与进度 |
| POST | `/api/v1/student/api-keys/{api_key_id}/models` | 为自己的生效 Key 添加已开放的模型 |
| POST | `/api/v1/student/api-keys/{api_key_id}/models/remove` | 移除自己 Key 上的模型授权 |
| POST | `/api/v1/student/api-keys/{api_key_id}/claim` | 一次性领取已批准 Key |

组织和项目列表可供学生浏览。项目列表返回真实状态和 `has_mentor`。提交申请时组织和项目须为 ACTIVE，项目须属于该组织。

模型广场将预置模型与学生个人模型库去重展示。广场及目录搜索展示启用、有正数倍率且已配置线路的模型。目录搜索选中的非预置模型可通过 `POST /api/v1/student/models` 收藏。

提交申请：

```json
{
  "name": "research-key",
  "organization_id": "uuid",
  "project_id": "uuid",
  "requested_monthly_credits": "20.00"
}
```

- 旧客户端可继续发送 `models`，服务端忽略该字段。新申请的模型授权为空。
- 服务端校验组织归属并存储 `project_id`。
- 同一学生进行中或生效 Key 名称大小写不敏感唯一；`rejected/revoked` 后可复用。

创建成功返回 `201 Created`：

```json
{
  "data": {
    "id": "uuid",
    "name": "research-key",
    "status": "pending_mentor",
    "progress": {"current":"mentor_review","completed_steps":["submitted"]},
    "organization": {"id":"uuid","name":"AI Lab"},
    "project": {"id":"uuid","name":"Research Assistant"},
    "models": [],
    "key_prefix": null,
    "claimed_at": null,
    "created_at": "2026-08-18T09:30:00Z"
  },
  "request_id": "01K2..."
}
```

详情额外返回按时间升序的 `audits`，每项含 `action`、`actor_role`、`comment` 和 `created_at`。不向学生返回审核人邮箱。

领取仅允许 `approved` 状态。成功时原子地切换到 `active`，并且只在本次响应返回完整 secret：

```json
{
  "data": {
    "api_key": "neb_sk_live_once_only",
    "key_prefix": "neb_sk_live_abc1",
    "claimed_at": "2026-08-18T09:30:00Z"
  },
  "request_id": "01K2..."
}
```

重复领取返回 `409 KEY_ALREADY_CLAIMED`。负责导师批准时自动建立学生的组织和项目成员关系。Key 停用后仍保有成员关系。

### 5.1 查看和修改 Key 的模型授权

Key 列表和详情保留全部模型授权。Key 的授权模型和模型广场条目都返回 `protocols`、`available_protocols`、`recommended_protocol`、`probe_state`、`availability`、`availability_reason` 和 `last_probe_at`。`protocols` 是已配置且凭据有效的协议，`available_protocols` 是最近探活通过的协议。`availability` 为 `available`、`unverified` 或 `maintenance`，与网关的判断一致。`last_probe_at` 是当前线路最近一次探活的 UTC 时间，含失败结果，缺少有效探活记录时为 `null`。学生目录搜索在 `raw_entry` 中返回这些状态字段。

`POST /api/v1/student/api-keys/{api_key_id}/models` 接受 `{"model_ids":["gpt-4.1-mini"]}`，每批 1 至 100 个公开模型 ID。添加要求本人的 `active` Key、有效的项目访问权限及模型报价。已有线路且待验证的模型可新增授权；维护中的模型返回 `MODEL_NOT_READY`。收藏与授权各自独立。

添加返回 HTTP 200。 `data.model_ids` 是当前完整授权列表，`data.added_model_ids` 是实际新增的模型。每个新加入的待验证模型在 `data.warnings` 中有一项 `{"model_id":"gpt-4.1-mini","code":"MODEL_UNVERIFIED","message":"已有绑定线路，当前还没有通过探活的协议。"}`。

`POST /api/v1/student/api-keys/{api_key_id}/models/remove` 接受同样的请求体。学生可移除自己 Key 的有效或历史授权，包括维护中的模型。HTTP 200 返回完整的 `data.model_ids` 和本次实际移除的 `data.removed_model_ids`。重复添加和重复移除返回空的变更数组。整批关联与审计写入同时提交或回滚，原 Key 的下一次网关请求立即读取更新后的授权。

| HTTP 状态 | 错误码 | 含义 |
| --- | --- | --- |
| 400 | `VALIDATION_ERROR` | Key ID 或请求体无效 |
| 403 | `FORBIDDEN` | 当前账号没有学生接口权限 |
| 404 | `RESOURCE_NOT_FOUND` | Key 不存在或不属于本人，或待添加模型不在平台目录 |
| 409 | `INVALID_STATE_TRANSITION` | 添加模型时 Key 尚未生效或项目访问资格已失效 |
| 409 | `MODEL_NOT_READY` | 新增授权的模型处于维护中或缺少报价，响应提供原因和模型 ID |

## 6. 导师接口

以下接口要求 `role=mentor` 且用户为 `active`。

| 方法 | 路由 | 用途 |
| --- | --- | --- |
| GET | `/api/v1/mentor/organizations` | 列出导师所属 ACTIVE 组织 |
| GET | `/api/v1/mentor/organizations/{organization_id}/projects` | 列出该组织全部 ACTIVE 项目并标明负责状态和申请状态 |
| POST | `/api/v1/mentor/project-applications` | 申请负责项目 |
| GET | `/api/v1/mentor/project-applications` | 查看自己的项目申请历史 |
| GET | `/api/v1/mentor/api-key-reviews` | 列出负责项目中待初审申请 |
| GET | `/api/v1/mentor/api-key-reviews/{api_key_id}` | 查看待初审申请详情 |
| POST | `/api/v1/mentor/api-key-reviews/{api_key_id}/approve` | 批准申请并分配项目额度 |
| POST | `/api/v1/mentor/api-key-reviews/{api_key_id}/reject` | 驳回申请 |
| GET | `/api/v1/mentor/projects/{project_id}/api-keys` | 列出负责项目的 ACTIVE Key |
| POST | `/api/v1/mentor/api-keys/{api_key_id}/revoke` | 撤销负责项目的 ACTIVE Key |

项目申请请求：

```json
{"project_id":"uuid"}
```

只能申请导师所属组织中的 ACTIVE 项目；同项目已有负责关系或 pending 申请时返回 `409 INVALID_STATE_TRANSITION`。

审核列表和详情展示申请学生的 `id/name/email`、组织、项目、额度及创建时间。任一负责导师可处理 `pending_mentor` 申请。并发后续请求返回 `409 INVALID_STATE_TRANSITION`。

通过请求接受 `{"monthly_credits":"20.00","comment":"optional"}`。允许批准零模型申请，事务校验项目和额度，写入成员关系、额度和审核记录，再切换状态为 `approved`。依赖失败时返回 `503 DEPENDENCY_UNAVAILABLE`，详情含 `{"operation":"api_key_approval","state_changed":false}`。驳回和撤销须提供原因。

## 7. 老师接口

以下接口要求 `role=teacher` 且用户为 `active`。老师拥有平台级管理权限，但没有项目成员或 ACTIVE Key 浏览接口。

### 7.1 路由

| 模块 | 方法 | 路由 | 用途 |
| --- | --- | --- | --- |
| 学生 | GET | `/api/v1/teacher/students?q=` | 列出全部学生账号及 mentor profile 状态 |
| 学生 | POST | `/api/v1/teacher/students/{student_id}/mentor-profile` | 授予导师身份（创建或激活 mentor_profiles） |
| 学生 | POST | `/api/v1/teacher/students/{student_id}/mentor-profile/revoke` | 撤销导师身份 |
| 邀请 | POST | `/api/v1/teacher/invitations` | 邀请老师 |
| 组织 | GET | `/api/v1/teacher/organizations` | 组织列表 |
| 组织 | POST | `/api/v1/teacher/organizations` | 添加组织 |
| 组织 | PATCH | `/api/v1/teacher/organizations/{organization_id}` | 修改名称、说明或状态 |
| 组织 | GET | `/api/v1/teacher/organizations/{organization_id}/mentor-candidates` | 搜索 ACTIVE 导师候选 |
| 组织 | POST | `/api/v1/teacher/organizations/{organization_id}/mentors/{mentor_id}` | 幂等分配导师到组织 |
| 项目 | GET | `/api/v1/teacher/projects` | 项目列表，可按 `organization_id/status` 筛选 |
| 项目 | POST | `/api/v1/teacher/projects` | 添加项目 |
| 项目 | PATCH | `/api/v1/teacher/projects/{project_id}` | 修改名称、说明或状态 |
| 项目申请 | GET | `/api/v1/teacher/mentor-project-applications` | 导师项目申请列表 |
| 项目申请 | POST | `/api/v1/teacher/mentor-project-applications/{application_id}/approve` | 批准并幂等加入项目 |
| 项目申请 | POST | `/api/v1/teacher/mentor-project-applications/{application_id}/reject` | 驳回，原因必填 |
| 供应商 | GET | `/api/v1/teacher/providers` | 供应商列表 |
| 供应商 | POST | `/api/v1/teacher/providers` | 添加供应商并加密凭据 |
| 供应商 | GET | `/api/v1/teacher/providers/{provider_id}` | 供应商详情，不返回凭据密文或明文 |
| 供应商 | PATCH | `/api/v1/teacher/providers/{provider_id}` | 修改名称、URL、状态；可替换凭据 |
| 模型 | GET | `/api/v1/teacher/models` | 全部模型去重视图与 `route_ready` |
| 模型 | POST | `/api/v1/teacher/models` | 主动添加模型 |
| 模型 | GET | `/api/v1/teacher/models/detail?model_id=...` | 按业务模型 ID 查询模型及 binding 详情 |
| 模型 | PATCH | `/api/v1/teacher/models/configuration` | 请求体携带 `model_id`，配置模型元数据、常用标记和状态 |
| Binding | POST | `/api/v1/teacher/model-bindings` | 请求体携带 `model_id`，添加供应商 binding |
| Matrix 目录 | GET | `/api/v1/teacher/model-catalog-candidates?q=&page=&page_size=` | 老师新增模型向导分页搜索 |
| 模型测试 | POST | `/api/v1/teacher/model-probes` | 自动读取供应商 `/v1/models` 并测试文本/检索端点，返回可应用的标准化配置 |
| Binding | PATCH | `/api/v1/teacher/model-bindings/{binding_id}` | 修改上游名、adapter、priority 或状态 |
| 北辰同步与对账 | GET | `/api/v1/teacher/providers/{provider_id}/beichen?day=YYYY-MM-DD` | 按北京时间查看同步状态、模型报价和日对账 |
| 学校级模型用量 | GET | `/api/v1/teacher/model-usage?range=day\|month\|all&provider_id=uuid` | 教师按模型、项目成员、学校 Key 查看北辰调用用量；供应商筛选可省略 |
| 北辰区间对账 | GET | `/api/v1/teacher/providers/{provider_id}/beichen/reconciliation?range=day\|month\|all` | 按模型和供应商渠道比较本地扣分、Token 与北辰日账 |
| 北辰调用对账 | GET | `/api/v1/teacher/providers/{provider_id}/beichen/reconciliation/calls?day=YYYY-MM-DD&cursor=…` | 按北京时间分页查看本地调用、缺失字段及已验证回执 |
| 北辰逐笔对账 | GET | `/api/v1/teacher/providers/{provider_id}/beichen/receipts?day=YYYY-MM-DD&cursor=…` | 教师按北京时间分页查看学校已归档的北辰流水及关联的校内扣分 |
| 北辰手动同步 | POST | `/api/v1/teacher/providers/{provider_id}/beichen/sync` | 执行一次有时限的同步，页面按数据类别显示各自结果 |
| 北辰 Key 网页核对 | POST | `/api/v1/teacher/providers/{provider_id}/beichen/key-usage` | 读取已确认的官网分组和该 Key 的网页流水，可同时记录页面的目标倍率 |
| 北辰手动探活 | POST | `/api/v1/teacher/providers/{provider_id}/beichen/probes` | 接受空请求体或 `{}`，排队后返回 202 |
| 北辰最近探活 | GET | `/api/v1/teacher/providers/{provider_id}/beichen/probes` | 查询任务进度和最多 100 个最近的 Key、模型、协议结果 |
| 启用北辰已发现模型 | POST | `/api/v1/teacher/providers/{provider_id}/beichen/models/enable` | 教师提交 `model`，如已有校内别名可另传 `school_model`。学校复核当前目录和路由后，使模型可供学生申请或调用。 |
| 批量启用北辰模型 | POST | `/api/v1/teacher/providers/{provider_id}/beichen/models/enable-batch` | 教师提交当前筛选出的 1 至 200 个模型，分别返回启用或跳过结果 |
| 按模型统一定价 | PATCH | `/api/v1/teacher/providers/{provider_id}/beichen/models/price` | 为同一模型的全部目录渠道设置教师价或恢复官网价 |
| 北辰采价授权 | GET / PATCH | `/api/v1/teacher/providers/{provider_id}/beichen/access/{credential_id}` | 读取配置状态，设置、轮换或停用该凭据的面板读取授权 |
| 北辰网页登录状态 | GET | `/api/v1/teacher/providers/{provider_id}/beichen/account` | 查看保存在该供应商下的网页账号及最近抓取摘要 |
| 北辰官网 Key 清单 | GET | `/api/v1/teacher/providers/{provider_id}/beichen/keys` | 读取上次保存的官网 Key 清单和同步状态 |
| 刷新北辰官网 Key | POST | `/api/v1/teacher/providers/{provider_id}/beichen/keys/refresh` | 用已配置的网页会话更新同一清单，返回更新结果 |
| 保存北辰网页登录 | POST | `/api/v1/teacher/providers/{provider_id}/beichen/account/login` | 提交账号密码或网页 Token |
| 验证北辰网页会话 | POST | `/api/v1/teacher/providers/{provider_id}/beichen/account/verify` | 在线检查会话状态，必要时续期密码模式的会话 |
| 生成旧版北辰本机授权 | POST | `/api/v1/teacher/providers/{provider_id}/beichen/account/export` | 供既有脚本使用，提交 `{"confirm":true}` 后返回 `beichen-session.env` |
| 清除北辰网页登录 | DELETE | `/api/v1/teacher/providers/{provider_id}/beichen/account` | 清除已保存凭证并尝试退出上游会话 |
| 抓取北辰官网目录 | POST | `/api/v1/teacher/providers/{provider_id}/beichen/crawl` | 手动更新官网目录，返回本轮抓取摘要 |
| 北辰校内试算价 | PATCH | `/api/v1/teacher/providers/{provider_id}/beichen/price` | 指定模型手动设价或恢复跟随 |
| 北辰报价或账单导入 | POST | `/api/v1/teacher/providers/{provider_id}/beichen/import` | 按凭据导入受支持的 CSV |

首期不提供任何 DELETE 接口。资源通过 `status=inactive/disabled` 停用。

### 7.2 管理请求

组织创建/修改字段为 `name`、`description`、`status`；项目创建字段为 `organization_id`、`name`、`description`，修改字段为 `name`、`description`、`status`。

老师邀请请求：

```json
{"email":"teacher2@example.com"}
```

迁移预置北辰供应商。学校部署随后从加密的发布配置建立四条渠道，以官网登录身份关联分组和数字 Key ID。教师模型页读取已同步的可用模型、当前首选线路倍率和启用状态。项目用量按学生、模型汇总。其他供应商可用现有接口配置。

学校 HTTPS 入口承载控制台、`/api/v1/` 及网关已有的原生协议路由。访问 `GET /teacher/providers`、`GET /student/api-keys` 或 `GET /login` 返回官方 Vue 应用的 HTML；应用使用同源 API 登录、选择身份和检查权限。登录挑战和刷新 Cookie 使用 Secure。读取网页本身不授予教师或学生权限。

通过 Compose 构建的控制台镜像从 0.10.3 起包含静态文件 `/.well-known/nebula-release.txt`，匿名 GET 返回构建时根目录 `VERSION` 的一行文本。该文件用于识别前端部署版本。维护用 HTTPS API 的证书和服务状态须通过该入口单独验证。

该 HTTPS 监听另有只读 `GET`、`HEAD /.well-known/nebula-maintenance`。返回 JSON 字段 `email_delivery`，值为 `log_only` 或 `smtp_configured`，不含邮箱、凭据或账号信息。匿名读取不会发送邮件。其他方法返回 405。

维护 TLS 上另有 `POST /.well-known/nebula-maintenance/certificate-notice`。请求使用 `application/json`，含 `email_digest` 和 `request_nonce` 两个 64 位小写十六进制字段。邮箱摘要为 UTF-8 字符串 `nebula-certificate-notice-v1:` 加规范化注册邮箱的 SHA-256。随机数由每次同意投递时单独生成。服务端根据 `LAB_MAINTENANCE_RELEASE`、`LAB_CERTIFICATE_NOTICE_REQUEST_SHA256` 与 `LAB_CERTIFICATE_NOTICE_EXPIRES_AT` 核对批准。配置值为 UTF-8 字符串 `nebula-notice-request-v1:<版本>:<邮箱摘要>:<随机数>` 的 SHA-256。收件人来自匹配的激活账号，邮件只含服务端版本、配置的入口与当前叶证书指纹。

格式正确统一返回无正文的 202，收件及投递状态仅能在邮箱确认。未启用或已过期返回 404，方法错误 405，错误媒体类型 415，跨来源浏览器请求 403。请求体上限为 512 字节，拒绝查询参数、Cookie、Authorization、额外字段及无效摘要。每次启动最多处理一个匹配请求，Redis 跨实例去重 24 小时。

供应商创建：

```json
{
  "name": "Provider A",
  "base_url": "https://api.provider.example/v1",
  "credential": "provider-secret"
}
```

`credential` 只作为写入字段；服务端加密后保存，所有读取响应只返回 `credential_configured: true/false`。

北辰接口限老师访问，凭据必须属于指定的北辰供应商。更换北辰 Key 时新增一条凭据并重新绑定，以保留原 Key 的账单归属。手动同步接受空请求体或 `{}`。概览中的 `datasets`、`models`、`comparison` 分别提供同步状态、报价和当日对账。`models[].catalog_source` 返回该目录行的来源（`key` 或 `website`）。成本的 `_micros` 字段以百万分之一北辰余额单位表示。`trial_credits` 和 `charged_credits` 用十进制字符串分别返回试算与实际结算积分，`pending_review_calls` 给出待核账笔数。未知值为 `null`，可确认的零单独返回。原有 `trial_credit_milli` 保留千分之一积分精度。

`credentials[].health` 包含 `credential`、`catalog`、`pricing`、`probe` 四个维度，各返回 `state`（`available`、`unavailable` 或 `pending`）和说明 `message`。`available_dimensions` 统计可用项。报价维度按当前可计算报价数判断。探活维度按有至少一种协议通过的模型占可探活模型的比例判断。

`model_catalog[].protocols` 使用网关绑定的协议枚举。`probe_state` 为 `unprobed`、`passed` 或 `failed`。同一模型任一当前 Key 和协议的最近结果通过即为 `passed`。`last_probe_at` 是所有有效最近结果里的最新时间。只有 `catalog_source=key` 的记录提供 Key 覆盖和探活依据；超过 30 分钟未刷新或扫描失败的目录保留展示，但 `probe_state` 回到 `unprobed`，不计入当前覆盖。官网来源和 `website_only` 型号不参与探活比例，也不显示探活结论。教师主表只列当前 Key 的 `/v1/models` 命中的模型，`website_only` 型号只在「官网目录」计数。发布可达线路数仍由 `ready_count` 表示。学校已启用但 `ready_count=0` 时返回 `binding_issue`，整个模型从当前目录消失时仍保留这个异常条目。`price_covered` 表示至少一条启用 Key 有可计算报价，发布时仍逐条检查实际会使用的线路。

线路就绪且报价阻止启用时，`model_catalog[].price_blocker` 返回 `{code, message}`，其中 `message` 说明涉及的字段及适用的档位号。常见代码包括 `missing_price`、`billing_mode_non_token`、`price_conditions_invalid`、`tier_missing_input`、`tier_missing_output`、`tier_range_invalid` 和 `effort_multiplier_invalid`。线路未就绪时，查看 `blockers` 中的 `no_ready_route`。

使用官网报价时，概览的 `models[].price_conditions` 保留官网原值。可按 Token 解析的报价在 `price_conditions_exact` 中显示实际使用的报价字段，数值以字符串返回以保留小数精度。空或省略的计费模式默认按 Token 处理，明确以 Token 为主时会忽略附带的按次、图像和视频单价。

选中历史流水反推报价时，`models[].price_status` 为 `derived`、`price_source` 为 `derived_from_usage`，`price_observed_at` 为反推时间。`price_conditions` 和 `price_conditions_exact` 给出这份基础单价，缓存价缺证据时省略对应字段。`upstream_rates` 已应用当前 Key 的已核实倍率。能力目录的 `price_state` 同样标记 `derived`。反推价只作审计和页面参考，不自动扣费：结算遇到没有教师手动价的反推价时，调用保持 `pending_review`，原因记为 `derived_price_requires_review`。教师确认正式官网价或保存手动价后，后续调用才可结算。

带上下文倍率的官网报价保留 `intervals` 中的四项倍率和档位标签。`price_conditions_exact` 将倍率应用到基础单价，并使用下限包含、上限不包含的边界。未命中该类档位时使用基础价。`website_directory.price_unit` 为「元（北辰余额单位）」。官网报价按 1:1 读取，界面上的 `$` 不触发货币换算。

`website_directory` 返回完整官网目录状态、`last_success` 和 `last_error`。`fresh` 表示近期完整采集，`stale` 表示当前使用部分更新或保留的快照，`unverified` 表示等待取得官网目录。`model_catalog` 合并官网与启用 Key 的目录，并保留已发布但失去目录覆盖的模型。`source` 为 `website`、`key` 或 `both`。`catalog_state` 为 `key_listed`、`website_only`、`published_unavailable` 或 `catalog_missing`。`key_count` 等于 `current_key_count`，只统计当前 `/v1/models` 的 Key 目录证据；`current_key_count` 按 `catalog_keys[].state=listed` 且非陈旧的 Key 去重，`stale_key_count` 统计扫描失败或超过 30 分钟未刷新、沿用上次结果的 Key，两者为零时省略。`catalog_keys[].stale` 标记这些保留条目；它们保留展示，但不计入 `current_key_count` 和 `key_count`，`probe_state` 也回到 `unprobed`。目录表 `provider_model_catalog.catalog_source` 区分 `key` 和 `website`，迁移 `0061_provider_catalog_source.sql` 新增非空列并默认 `key`，同时把旧 `raw_entry->>'source'='beichen_website_group'` 的行回填为 `website`。

`catalog_state=published_unavailable` 表示该模型曾发布（校内模型已启用、存在绑定或保留的绑定异常），但当前启用的渠道 Key 都没有在 `/v1/models` 列出它。该行保留在主表异常筛选中，`binding_issue.code` 为 `catalog_missing`；如果只剩陈旧的 Key 目录记录则为 `catalog_stale`。它不会转成 `website_only`，也不提供可启用的 Key 覆盖。`website_only` 型号只汇总在 `website_only_models`，不进入主表。

`website_directory.last_run` 是最近一次后台抓取的摘要，尚无运行记录时为 `null`。摘要包含 `started_at`、`duration_ms`、`platform_count`、`group_count`、`model_count`、`expected_model_count` 和 `failures`。同一平台出现在不同渠道下时分别计数。模型按去掉首尾空白的官网原名去重，保留大小写及版本差异。分组按照分组 ID 去重，没有 ID 时用渠道内的平台和分组名识别。`expected_model_count` 含原响应中所有 `models` 和 `supported_models` 数组中能辨认的型号。当平台资料损坏时，本轮成功归入平台的模型数可能低于它。失败项提供 `path`、`reason` 以及可读取的 `channel_id`、`channel` 和 `platform`。

`website_directory.last_run.key_model_comparisons` 按学校渠道返回 `/v1/models` 与当前官网分组的对照。`matched` 提供 `key_model`、`channel_model`、匹配方式和 `price_complete`。名称相同优先，其次核对可以双向唯一匹配的写法变体。`only_key` 和 `only_channel` 保留各自的官网或请求原名。`state=unavailable` 表示 Key 目录读取失败，`unverified` 表示分组或渠道证据不足。两种状态都不把未知结果当作已确认差异。整批身份读取失败时摘要提供 `key_model_error`。官网分组的 `account_rate_multiplier` 来自同一网页登录会话的 `GET /api/v1/groups/rates`，`rate_multiplier` 为分组默认值。账户倍率未读到时，`last_run.account_rate_error` 给出状态。页面展示分组倍率 `group_rate`，结算采用账户倍率 `account_rate`；两者不同时以账户倍率为准，分组倍率只作对照。

`website_locations` 带官网原名、渠道、平台、分组、采集时间、缓存状态、`price_scope` 和报价存在标记。`price_scope=platform` 表示平台级共享基础价，可用于同一平台同名型号的跨组回退；`price_scope=group` 表示分组专属价，只能用于该分组本身；缺少该字段的旧数据按分组专属价处理。`price_complete` 表示该官网报价具备所声明计价方式的必要单价，`has_price` 表示至少找到一个数值单价。`catalog_keys` 带实际覆盖的 Key 及上游型号，或者给出需要核查的 Key。`state` 可为 `listed`、`missing`、`pending`、`failed` 或 `ambiguous`。已覆盖条目用 `match` 说明原名、名称归一或教师别名，并保留独立的 Key 型号 ID 供教师操作。弱匹配可以识别唯一的日期版本，保留型号数字和具有实际含义的变体。当前 Key 的报价待确认时，官网价栏显示已读到的完整报价及其所属分组。当前 Key 分组没有自己的完整官网价表时（如分组 16），上游基础价取同一平台公开的 `group_id=0` 同名型号报价，再乘当前 Key 生效倍率 0.60；其他分组的专属报价不当作基础价。学生实价继续乘学校 1.10 系数。

`catalog_differences` 按启用 Key 返回已知分组、核对时间、`website_only` 和 `key_only`。官网分组与当前 Key 目录均可对比时标为 `compared`。比较使用保留的官网数据时标为 `stale`。缺少一方证据时标为 `unverified`。`website_only_models` 汇总当前 Key 均未匹配的官网型号及候选渠道，`website_only_status` 表示该汇总的核对状态。

探活 POST 返回任务对象，GET 返回 `{run, results}`。概览中的 `probe_run` 使用相同任务对象。任务对象含 `state`（`idle`、`queued`、`running`、`completed` 或 `failed`）、`started_at`、`finished_at` 和 `error_code`。同一供应商在排队或执行时收到重复 POST 会合并。没有启用且已同步目录的 Key 时 POST 返回 400，供应商不存在或并非北辰时返回 404。未登录返回 401，角色不符返回 403。两个接口均返回 `Cache-Control: private, no-store`。

`results` 按 `checked_at` 倒序，每个当前组合只保留最新结果。每项包括 `provider_id`、`credential_id`、`model`、`protocol`、`status_code`、`error_code`、`duration_ms`、`checked_at` 和 `detail`。尚未收到 HTTP 状态时 `status_code=0`，通过时 `error_code=null`。`detail` 只记录输出限制 `max_output_tokens: 1`。

维护用的 `beichen/access/{credential_id}` 可读该凭据的同步状态、分组与北辰 Key 数字 ID。学校自动部署通过官网普通账号建立这些关联。旧版专用读取凭据的接口保留兼容。

北辰网页登录接口使用教师身份和标准响应封装。提交凭证使用可信 HTTPS 入口，JSON 请求体为 `{"mode":"password","email":"…","password":"…"}` 或 `{"mode":"token","token":"…"}`。账号状态返回 `configured`、`mode`、`email`、`status`、`expires_at`、`last_verified_at`、`last_error`、`failure_count` 和 `crawl`。密码及访问、续期凭证只用于上游请求。状态为 `active`、`expired`、`error` 或 `none`。没有可用数据的时间和错误字段为 `null`。

登录和验证如已写入业务失败状态，仍返回该账号状态对象，中文原因在 `last_error` 中。Token 模式提交后可用「验证」检查当前访问权限。`crawl` 包含 `last_run_at`、`platforms`、`models`、`failures`。后面三项为计数，尚未抓取时均为 `null`。有失败项时，`failure_details` 包含渠道、平台、字段位置和失败原因。手动抓取返回本轮摘要。参数错误返回 `400`，供应商不存在返回 `404`，已有抓取正在运行时返回 `409`，上游或数据库不可用时返回带中文原因的错误。

本机窗口的 HTTP 服务仅监听 `127.0.0.1` 的随机端口。浏览器通过同源 WebSocket 完成登录、刷新、测试抓取和关闭，连接和后续操作各使用一次性随机校验值。已部署的旧版脚本可继续向 `POST /api/v1/beichen/local-session/{provider_id}` 提交学校本机授权以读取短期北辰访问 Token。只有有效授权、启用的供应商和仍在职的授权教师才能使用该接口。再次生成授权或替换、清除网页登录配置会撤销旧授权。

自动同步和按供应商的手动同步独立刷新完整官网目录，从登录后的 Key 清单核实分组，再读取该分组的模型、报价和倍率规则。教师模型页的「用指定 Key 重扫」向同步接口提交 `credential_id`，同步该 Key 的账单、目录、报价和近期用量。`/beichen/catalog/rescan` 为单独读取 `/v1/models` 的维护接口，只刷新该 Key 的模型清单。学校预配渠道优先使用学校网页会话，网站提前拒绝会话时自动续期后重试一次。手动同步遇到官网目录或采价失败会返回同步未完成。成功保存的结果可在概览查询，价表错误记录在 `datasets`。教师换 Key 进度中的 `sync.usage` 对应单 Key 网页用量同步。

`datasets` 的 `billing` 条目包含 `billing_state` 和 `effective_multiplier`。有对应记录时还包含保留小数精度的 `effective_multiplier_text`、`group_id`、`group_rate`、`peak_enabled`、`peak_rate` 和倍率版本的 `observed_at`。`group_rate` 是官网分组默认倍率，用于展示；`effective_multiplier` 优先取账户倍率 `account_rate`。两者不同时，页面并列提示分组倍率和账号倍率，结算只采用账号倍率。账户倍率未核实时不把分组默认倍率当作可结算倍率，`billing_state` 为 `stale` 或 `unverified`，新调用保持待核。`current` 表示最近一次读取已确认，`stale` 保留上次倍率和 `last_success`，`unverified` 表示还没有可用依据。连续失败记录在 `failure_count`，最多记到六次，达到三次时 `alert` 为真。

`catalog_keys[].group_diagnosis` 的 `model_outside_key_group` 表示型号在其他官网分组且当前 Key 的目录未列出，`price_outside_key_group` 表示型号有其他分组报价、当前 Key 分组缺价，`group_rate_mismatch` 表示同一分组的官网与 Key 计费规则不一致。`website_locations[].groups` 返回官网分组倍率和可用的高峰规则。`website_locations[].price_conditions_exact` 以十进制字符串保存可显示的单价数字，供报价对比使用。Key 分组沿用陈旧记录时，`group_stale` 为真。

概览的 `datasets[]` 中，`kind=billing` 可返回从 Key 清单核实的 `group_id`、`group_name`、`group_rate` 和 `group_status`。`pending` 表示归属待同步，`assigned` 表示清单确认绑定分组，`not_found` 表示已匹配的 Key 明确未绑定分组，`not_allowed` 表示计费或模型清单接口明确返回 `GROUP_NOT_ALLOWED`。`not_allowed` 保留清单确认的分组。分组状态与 `billing_state` 分别记录归属和生效倍率的核实结果。Key 计费和模型清单已核实的同步可以成功提交，缺少价表另由 `kind=prices` 的 `official_price_missing` 表示。已具备反推单价的模型用 `price_status=derived`、`price_source=derived_from_usage` 和 `price_observed_at` 标明来源与时间。

本轮已在当前分组的有效价表中查找该模型，而价表缺少同名条目时，概览的 `models[].price_status` 返回 `not_listed`。当前分组整组缺少官方价表时，有公开官网同名基础价的型号按该基础价计价并标 `confirmed`；只有历史流水的型号标 `derived`，仅作审计。分组待核、价表缺失或过期、已列出的同名条目缺少有效价格，通常返回 `pending_confirmation`。

概览的 `archived_usage` 以北京时间按日、模型和凭据汇总持续采集的原始流水。`rows`、`unmatched_rows`、`unpriced_rows` 分别表示已采集笔数、未能归属学校扣款的笔数和无法确认余额实扣的笔数。`observed_cost` 汇总已知原始实扣，`matched_cost`、`matched_base_credits`、`matched_charged_credits` 与 `margin_credits` 只比较同一批可匹配且金额齐全的调用。官网更正金额后会更新差额，保留原学生扣分。

`website_sync` 汇总当前学校预配的有效渠道。按 `channels` 查看渠道总数，`priced_channels` 表示当前有有效网页报价的数量，`price_last_seen` 表示最近观察到网页模型价格的时间。`receipt_checked_channels` 计数曾成功读取逐笔流水分页的渠道，`receipt_last_checked` 为最近成功读取一页的时间，`receipt_caught_up_channels` 计数已读到末页的渠道。时间可为空。这组状态反映当前同步进度，查询不同账日时含义相同。

该概览响应使用 `Cache-Control: private, no-store`。通过学校维护 HTTPS 登录的指定教师可收到 `maintenance_workspace` 对象。对象为空表示等待校方提供远程应用地址，配置完成时包含 `launch_url`。其他账号的概览省略此字段。该字段只提供导航，网页授权状态和采集进度分别由 `web_login_required` 与 `website_sync` 表示。

逐笔对账的 `day` 默认为北京时间当天。第一页省略 `cursor`，后续传入上页的 `data.next_cursor`，保持同一供应商和日期。每页最多 50 行，以流水发生时间倒序排列。最后一页的游标为 `null`。游标错用到其他账日或供应商、游标内容错误及日期格式错误均返回 400。每行含 `occurred_at`、`model`、`matched`、`student_name`、`base_credits`、`school_multiplier`、`charged_credits`、`actual_cost` 和 `difference_credits`。金额和积分用十进制字符串表示。缺少依据的字段返回 `null`，确认零金额返回 `"0"`。差额等于学校已扣积分减去北辰原始实扣乘 100。此查询只读取学校的账本。

按凭据的旧价格请求包含 `credential_id`、`model`、`mode`。设置 `manual` 时同时提供 `rates`，其中 `input`、`output` 必填，`cache_read`、`cache_write` 可空，四项均以百万分之一校内积分/百万 Token 表示。恢复 `follow` 时省略 `rates`。教师页按模型统一定价使用下面的新接口，其请求直接使用积分十进制字符串，不能混用单位。新调用的教师基础价参与正式扣费，再应用请求发出时保存的学校系数；已入账调用保持原金额。

#### 北辰官网 Key 清单

清单和刷新接口要求教师登录。GET 只读取学校数据库。POST 使用已配置的网页会话读取北辰 `GET /api/v1/keys?page=1&page_size=100` 并检查分页完整性。密钥页仅给出分组 ID 或分组明细不全时，由 `GET /api/v1/channels/available` 按同一个 ID 补全。

`data.status` 为 `unsynced`、`current`、`stale`、`session_expired` 或 `unavailable`。成功时 `last_seen` 更新为读取时间。北辰返回 401 或会话已知过期时返回 `session_expired`，保留最近成功的清单及时间。已保存的清单超过 30 分钟未更新时显示 `stale`。并发刷新返回 `invalid_transition`。

`data.keys` 的每一项包含北辰 `id`、`name`、脱敏后的 `key_prefix`、`group_id`、`group_name`、分组倍率 `rate`、`status`、`enabled`、`created_at`、`last_seen`、匹配的学校 `credential_id` 和 `match_status`。`peak` 包含 `enabled`、`rate`、`start` 和 `end`，取官网 Key 页的 `group.peak_rate_enabled`、`group.peak_rate_multiplier`、`group.peak_start` 和 `group.peak_end`。倍率用十进制字符串传输，未知字段为 `null`。匹配状态为 `matched`、`unmatched` 或 `ambiguous`。

`observed_at` 是当前分组、倍率和高峰规则版本首次读到的时间。`changed_at` 是当前版本与上一版本发生变化时的抓取时间，首次建立基线或尚未采集版本时为 `null`。`last_seen` 随每次成功读取推进，便于判断数据是否新鲜。两次抓取之间发生的变化以实际读到的时间登记。

`beichen-crawl --keys` 的 `keys[].models` 来自 `channels/available` 的同一分组，`actual_models` 和 `actual_model_count` 来自使用当前 Key 发出的 `GET /v1/models`。`matched` 同时保留 Key 型号原名和渠道型号原名，名称完全一致或写法可以双向唯一匹配时成立。`only_key` 和 `only_group` 分别列出只在实际清单或只在官网分组出现的型号。一侧资料无法核实时，差异字段为 `null`。

`model_check=checked` 表示读取到非空清单，`empty` 表示上游明确返回空清单。`unverified` 表示官网未提供可请求的完整 Key，`unavailable` 表示模型读取失败，两者的实际型号数和清单均为 `null`。此时命令保留其余 Key 的结果并返回非零退出码。用于查看型号的完整 Key 只在本次请求期间留在内存。

##### 接口稳定性清单

| 接口 | 稳定性 | 用途 |
| --- | --- | --- |
| 北辰 `GET /api/v1/keys` | `experimental` | 上游网页接口，分页和分组字段按当前实测兼容 |
| 北辰 `GET https://api.beichenapi.com/v1/models` | `experimental` | 用该把 Key 授权读取型号清单，显式空列表与授权失败分别处理 |
| `GET /api/v1/teacher/providers/{provider_id}/beichen/keys` | `stable` | 教师读取当前清单、倍率、高峰规则及最后变化时间 |
| `POST /api/v1/teacher/providers/{provider_id}/beichen/keys/refresh` | `stable` | 手动触发与定时抓取共用的同步流程 |

### 7.1 北辰批量启用与按模型定价

这两个接口仅允许教师访问，使用控制面响应 envelope。不存在或非北辰的 `provider_id` 返回 `404 RESOURCE_NOT_FOUND`；未登录为 401，非教师为 403。JSON 未知字段、模型名无效、数组长度或价格格式错误返回 `400 VALIDATION_ERROR`，数据库不可用返回 503。

`POST .../beichen/models/enable-batch` 请求为 `{"models":["model-a","model-b"]}`，每批 1 至 200 个模型，名称不超过 256 字符，重复名称忽略大小写去重。响应 `data` 为：

```json
{
  "enabled": [{"model": "model-a", "model_id": "uuid"}],
  "skipped": [{"model": "model-b", "reason": "no_price", "message": "第 2 档缺少输出 Token 单价（output_price）"}]
}
```

`reason` 包括 `not_found`、`already_active`、`route_not_ready`、`no_price`、`enable_failed`。各模型独立提交，单项失败不会撤销已成功的其他模型；重复请求返回已启用的跳过结果。可计算的纯 Token 条件价可用于启用，空或省略的 `billing_mode` 默认按 Token 处理。单个启用的错误提示和批量结果的 `message` 使用与概览相同的报价说明。

`PATCH .../beichen/models/price` 请求示例：

```json
{
  "model": "model-a",
  "mode": "manual",
  "rates": {"input": "150", "output": "750", "cache_read": "15", "cache_write": "0"}
}
```

新接口的 `rates` 是**积分/百万 Token 的十进制字符串**，不是 JSON 数字，也不是旧接口的百万分之一积分整数。输入与输出必填，缓存项省略按零；最多六位有效小数，范围为 0 至 1000000。服务端精确乘 1000000 后存储，不用浮点数转换。教师价是未乘学校系数的基础价，不再乘渠道倍率；学校渠道通常再乘 1.10 扣学生积分。`{"model":"model-a","mode":"follow"}` 恢复官网价且必须省略 `rates`。

同一模型在供应商当前目录下的全部渠道在一个事务、同一生效时刻写入。教师价持续生效，直到主动恢复跟随；官网同步继续保存报价，不会覆盖教师选择。响应为 `{"saved":true,"credentials":[{"credential_id":"uuid","label":"渠道名称"}]}`，没有该目录模型时为 404。只影响保存之后派发的请求，不追改历史扣款。

### 7.2 学校级北辰用量与区间对账

**存量待核回放与结算**

教师专用 `POST /api/v1/teacher/providers/:provider_id/beichen/pending-audit` 的成功和错误响应均返回 `Cache-Control: private, no-store`。发送 `{"apply":false,"limit":200,"offset":0}` 获取预览。按创建时间和 ID 升序，limit 默认 200、上限 500，offset 默认 0，负数返回 422。将 next_offset 用于读取下一批，末批返回 null。确认时发送相同 offset、有效 limit、`apply:true` 和本批 preview_token。

响应 data 含 provider_id、generated_at、backlog_total（该供应商预览时全部待核数，含无派发价格记录）、total（本批笔数）、next_offset、exact/narrow/wide/unknown、auto_settle、max_error_yuan、estimated_auto_yuan、preview_token 和 rows。样本包含 id/student/model/protocol/reason/class/min/max/recommended/note、can_auto_settle 及可结算项的 student_credits。金额区间和推荐金额单位均为上游元。缺少派发价格时 class 为 unknown，note 说明原因，保留待核。

结算结果 charged/still_pending/skipped/errors 为本批笔数。无法自动结算的行计入 still_pending。credits_charged 为实际扣除积分的精确十进制字符串，remaining_pending 为结算后该供应商全部待核数，含无派发价格记录。缺少令牌、跨页使用或确认时快照不符均返回 409 pending_audit_changed；批次外新增待核也会使令牌失效。结算后从首页重新回放。

`GET /api/v1/teacher/model-usage` 与 `GET .../beichen/reconciliation` 仅允许教师，均返回 `Cache-Control: private, no-store`。`range` 省略或为空时为 `all`；只接受 `day`、`month`、`all`，其他值返回 **422 `VALIDATION_ERROR`**（`details.field=range`）。无效 UUID 返回 400；显式指定不存在或非北辰供应商返回 404；未登录 401、角色不符 403、数据库不可用 503。

共同顶层字段为 `range`、`start`、`end`、`timezone`。时区固定 `Asia/Shanghai`，时间以 RFC 3339 UTC 返回，区间左闭右开。`day` 是北京时间当天 00:00 至次日 00:00；`month` 是本月 1 日至次月 1 日；`all` 的 `start=null`、`end` 为查询时刻。总计基于当前数据库中仍保存的业务记录，不补造已经清理的历史 Token。

学校用量可选 `provider_id`；省略时汇总所有北辰供应商，其他供应商调用不计入。调用归属沿用日对账的派发渠道、上游模型和发送时刻；只计已完成结算流程的调用，排除 `outcome=pending`。

| 学校用量字段 | 内容 |
| --- | --- |
| `totals` | `credits`（积分字符串）、`tokens`、`input_tokens`、`output_tokens`、`cache_read_tokens`、`cache_write_tokens`、`calls`、`pending_calls`、去重 `students` / `keys` 数量 |
| `models[]` | `model` 与上述用量字段，按模型名排序 |
| `students[]` | `user_id`、`user_name`、`project_id`、`project_name` 与用量字段；同一账号在不同项目分行，`models[]` 给出各模型积分、Token 和调用数 |
| `keys[]` | `api_key_id`、`api_key_name`、`user_id`、`user_name`、`project_id`、`project_name` 与用量字段，另含该 Key 的 `models[]` |

`credits` 累计 `beichen_state=charged` 的精确扣分。已回报的 Token 独立于扣费状态汇总，包括待核或中断调用已记录的分项。OpenAI Chat、Responses 和 Realtime 的原始输入包含缓存，依据明确的缓存明细或未命中数拆分。Anthropic 的原始输入是未缓存输入。Gemini 的已知输入包含工具提示，已知输出包含思考 Token。缺少互斥分项时，`tokens` 包含可以从原始总量或协议计数确认的用量，`tokens_missing` 表示其中尚未归入四类的数量。结合 `completeness` 判断覆盖范围。Token 和计数为 JSON 数字，积分为十进制字符串。

调用对账的 `day` 默认是北京时间当天，响应为 `items` 和可空的 `next_cursor`。每页最多 50 条，以调用派发时间、调用 ID 倒序排列。后续页传回此前的 `next_cursor` 并沿用同一供应商和账日。接口仅供教师读取，返回 `Cache-Control: private, no-store`。

逐笔记录含 `request_id`、协议、模型、派发渠道与 `usage_state`。`reported` 表示四项可确认，`partial` 表示可用计数不完整或存在矛盾，`missing` 表示没有可用计数，`interrupted` 表示响应中断。`recorded_usage_state` 保留历史记录的原始状态。`local_tokens` 与 `receipt_tokens` 分别提供未缓存输入、输出、缓存读、缓存写。不可用字段为 `null`，逐项原因见 `missing_token_reasons`。原始输入、输出、显式总计、思考与工具提示计数单独返回。

`base_credits` 是已记账的基础积分，`expected_credits` 是可追溯到独立计价的基础积分，`actual_credits` 是唯一匹配的北辰回执实扣乘 100。`difference_credits` 等于独立计价减回执实扣积分，`base_difference_credits` 等于已记账基础积分减回执实扣积分。缺少任一依据时，对应差额为 `null`。学校系数、学生实扣、学校承担、档位及价格版本随逐笔记录返回。已用回执实扣补记的跟随价行只有 `base_credits`，独立计价为空。此接口的逐笔差额使用通过校验的回执，区间利润仍以完整日账为依据。

区间对账的 `end` 固定为北京时间次日 00:00，汇总与未关联流水均排除该时刻及之后的数据。`range=all` 的 `start` 为 `null`，其他范围从对应起点开始。

`unmatched_scope` 默认为 `observed`。传入 `comparable` 时只返回 `coverage.eligible_dates` 内的流水，非法口径返回 422。第一页省略 `unmatched_cursor`，后续页传回 `unmatched_next_cursor`，并沿用供应商、范围及口径。跨北京时间零点或严格视图的入选日期变化后，从第一页重新查询。

| 对账字段 | 内容 |
| --- | --- |
| `totals.local` | `requests`、`credits`、`pending_calls`、`tokens` 及四项 Token 拆分、可空 `estimated_micros`（本地按调用时有效上游价加权估算的微元） |
| `totals.upstream` | `requests`、`tokens` 及四项 Token 拆分。`actual_cost` 是精确实扣字符串，`unit` 为已知的上游原单位，`billed_micros` 兼容旧日账。`source` 是按字母排序去重的来源，多个来源以逗号连接。`mixed_units` 为真时按 `amounts_by_unit` 分别返回各单位金额。`stale` 标示所用金额含陈旧观测。 |
| `totals.money` | `student_yuan`、`base_yuan` 为十进制字符串；`actual_yuan`、`margin_yuan`、`margin_rate` 为十进制字符串或 `null` |
| `models[]` | `model`、`local`、`upstream`、上述五个金额字段、`local_share` / `upstream_share`，按模型名排序 |
| `keys[]` | 按供应商凭据聚合的 `credential_id`、`label`、`local`、`upstream`、金额、占比；不是学生的学校 API Key，已删除渠道的标签可空，历史账单仍保留 |
| `models[].match_status` / `keys[].match_status` | `matched`、`local_only`、`upstream_only`，表示该分组两侧是否有记录，不表示逐笔账单已全部核对成功 |
| `coverage` | `days_expected`、`days_present`（按“渠道 × 账日”计数）、去重 `missing_days` 日期数组和逐渠道 `credentials[]`。`key_usage_days`、`key_usage_model_days`、`stale_key_usage_days` 分别记录使用网页 Key 账日数、附有已核对模型实付的账日数、沿用陈旧快照的账日数。另含 `pending_calls`、`unpriced_calls` 和 `multiplier_mixed`。 |
| `coverage.eligible_dates` / `comparable_only` | 已结束、同步完整且有可比较实扣金额的账日，以及这些日期的汇总。没有可比日期时汇总字段为 `null`。 |
| `unmatched_upstream` | 每页最多 50 条未取得已验证扣款关联的归档流水。`local_source=unique_claim` 表示有唯一认领，`local_status` 提供可读取的本地状态。认领可在回执明细缺失时单独显示，此时 `receipt_id` 为空。 |
| `unmatched_total` / `unmatched_next_cursor` / `unmatched_scope` | 当前口径的流水总数、下一页游标和当前口径。末页游标为 `null`。 |

本地侧与学校用量使用同一归属和 Token 口径。每个渠道账日依次选用人工导入、确认完整的长期归档流水和官网单 Key 快照，归档标记 `website_receipts`，近期快照标记 `key_usage`。历史归档可独立确认账日，核对范围包括已停用渠道。账日是否完整取决于官网核对结果。日和模型实付均汇总网页流水的 `actual_cost`。明确回报的零提供 `"0"`，缺失金额或混合单位的总金额为 `null`。按模型和按渠道分别合并两侧，保留仅一侧存在的记录。`estimated_micros` 需要完整报价和用量。

学校金额按 **100 积分 = 1 元** 换算。`student_yuan` 取已扣积分乘 0.01，`base_yuan` 取保存的基础积分或按该笔学校系数反推后的折算值。`actual_yuan` 适用于同为 CNY 的上游金额或沿用旧日账口径的数据。金额差额需要完整日账、同一金额单位及未陈旧的实扣。USD 等原单位通过 `unit` 返回，`actual_yuan`、`margin_yuan`、`margin_rate` 为空。`local_share` / `upstream_share` 分别是模型或渠道 Token 占对应侧总 Token 的 0 至 1 十进制字符串，总 Token 为零时为 `"0"`。

覆盖范围从每条当前供应商凭据的北京时间创建日与所选区间起点较晚者开始。在用渠道要求同步至今天，停用渠道要求同步至停用当天，两者均含当天。`beichen_usage_days` 中明确确认的空账日也算已同步。`missing_days` 按日期排序且去重，`credentials[]` 给出各渠道的 `credential_id`、`label`、`expected_days`、`synced_days`、`missing_days`，可定位同一天缺哪条渠道。`unpriced_calls` 是已扣款但未保存基础积分的调用数，`multiplier_mixed` 表示范围内出现过不同学校系数。尚未整日确认的今天可能显示缺失，日账覆盖不等同于分钟级流水扫描进度。

#### Key 网页核对

`POST .../beichen/key-usage` 使用教师身份，要求启用的 `credential_id`。可提交 `{"credential_id":"渠道 UUID","target_multiplier":"0.6","expect_claude":true}`。两个目标字段均可省略，空倍率字符串可清除目标倍率。成功返回 `{"synced":true}`。并发核对同一 Key 时返回状态冲突。网页读取失败返回 503，本次已验证的分组或已读完的账日仍可保存。

北辰网页请求使用会话 Token。先用 `GET /api/v1/keys` 确认 `id`、`user_id` 和 `group_id`，再用 `GET /api/v1/channels/available` 读取该组型号、价格和 `rate_multiplier`。用量使用 `GET /api/v1/usage`，传 `api_key_id`、同一天的 `start_date` 和 `end_date`、`timezone=Asia/Shanghai`、分页参数和 ID 升序。每笔必须匹配账号、Key 和日期。`total_cost` 可作为网页原价字段，实付取 `actual_cost`。教师概览的 `key_capabilities[]` 包含 `website_key_id`、`website_group` 和 `group_status`。分组状态与计费同步共用。`website_group` 返回核实的 ID、名称和倍率，缺少分组资料时为 `null`。

`GET .../beichen?day=YYYY-MM-DD` 响应的 `key_capabilities[]` 按渠道提供 `models`、`has_claude`、`balance`、`unit` 和模型、用量各自的状态、时间与错误。`daily_usage` 为所选北京时间日期的数据。`model_stats` 给出模型分项，模型日期未确认时 `model_stats_day` 为空。`observed_multiplier` 等于 `actual_cost/cost`，原价缺失或为零时留空。`multiplier_diff` 表示相对 `stored_multiplier` 的偏差严格超过 5%。金额和倍率以十进制字符串返回。`group_check` 为 `old_group`、`mismatch`、`target_observed` 或 `pending`。

导入请求包含 `credential_id`、`kind`、`csv`。报价表头为 `model,input,output,cache_read,cache_write`，价格为每百万 Token 的百万分之一北辰余额单位。用 `prices` 导入已应用倍率的最终报价，用 `prices_base` 导入需乘北辰倍率的已确认基础报价。日用量 `usage` 表头为 `date,model,requests,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,billed_upstream_micros`，日期为北京时间，缓存和实际扣款留空表示缺失。

`usage_export` 接受北辰用量页的逐笔导出原始表头，并要求 `full_day_confirmed: true`。先在北辰选择一把 Key 和一个已结束的完整北京时间账日，取消模型、请求类别等可能使记录不完整的筛选，再导出。每行 `Time` 须含时区，换算出的北京时间日期须一致。每行 `API Key Name` 须与所选供应商凭据标签匹配。行数和文件大小仍受 CSV 限制。服务按模型汇总 Token 和以裸十进制表示的美元实付 `Billed Cost`，再统一换算成微美元。某字段在任一记录中缺失时，该模型日汇总的字段保持为空。记录内的 IP 和请求细节不入库。

明细导入只用于补账。文件内容可以验证日期，覆盖整个账日仍需导入者核对筛选范围。所选 Key 已在账日结束后取得官网快照时，拒绝再导入同日的逐笔文件。补账可替换同日、同模型的旧补账，已有手工汇总优先。成功同步官网日账后保留原有导入记录。概览的 `upstream_usage_source` 沿用 `usage`（官网 Key 快照或已确认归档）、`csv`（手工汇总）、`export`（逐笔导入补账）或 `null`（来源缺失）。

模型创建/修改使用核心模型字段。将模型改为 `active` 前必须至少存在一个 ACTIVE binding，且对应 provider 为 ACTIVE。更新请求中的 `credit_multiplier` 只有在已有倍率且值确实改变时才要求 `multiplier_change_reason` 并写倍率审计；原值为 NULL 的首次设置默认倍率不要求原因。Binding 请求形状：

`context_window`、`max_input_tokens` 和 `max_output_tokens` 在 PATCH 中支持三态：省略保持不变、`null` 清空、正整数更新；它们只表示模型容量，不参与计费。`model_id` 创建后只读。能力只允许 `reasoning`、`vision`、`tool_calling`、`structured_output`、`web_search`、`coding`、`embeddings`、`rerank`、`realtime`、`image_generation`、`video_generation`、`speech_to_text`、`text_to_speech`。激活模型、停用 provider 或停用 binding 会在事务内锁定相关路由记录；若操作会令 ACTIVE 模型失去最后可用路由，返回 `409 MODEL_ROUTING_REQUIRED`，并在 `details.model_ids` 给出受影响模型。

```json
{
  "provider_id": "uuid",
  "upstream_model_name": "upstream-model",
  "adapter": "openai_compatible",
  "priority": 100,
  "status": "active"
}
```

`adapter` 可选值及职责：

| Adapter | 公共协议入口 | 上游协议 |
| --- | --- | --- |
| `openai_compatible` | `/v1/chat/completions`、`/v1/completions` | OpenAI Chat/Completions |
| `openai_responses` | `/v1/responses`、`/v1/responses/compact` | OpenAI Responses |
| `openai_embeddings` | `/v1/embeddings` | OpenAI Embeddings |
| `openai_images` | `/v1/images/generations`、`/v1/images/edits`、`/v1/images/variations` | OpenAI Images |
| `openai_audio` | `/v1/audio/transcriptions`、`/v1/audio/translations`、`/v1/audio/speech` | OpenAI Audio |
| `openai_video` | `/v1/videos`、资源查询、内容下载和 remix | OpenAI Videos |
| `openai_realtime` | `/v1/realtime` | OpenAI Realtime WebSocket |
| `openai_moderations` | `/v1/moderations` | OpenAI Moderations |
| `anthropic` | `/v1/messages` | Anthropic Messages |
| `cohere_rerank_v2` | `/v2/rerank` | Cohere Rerank v2 `/v2/rerank` |
| `google_gemini_v1beta` | `/v1beta/models/{model}:...` | Google Gemini 原生 `v1beta` |

同一模型需要支持多个协议时，应分别创建对应 adapter 的 Binding；网关不会在协议之间自动转换请求或响应。

老师负责配置平台模型和绑定。学生领取 Key 后可以给原 Key 添加已开放的模型。

## 8. 网关 API

网关不使用控制面 envelope，成功和失败响应保持对应 OpenAI、Anthropic、Cohere 或 Google Gemini 协议原生格式。OpenAI 与 Anthropic 入口位于标准 `/v1`，Cohere Rerank 保持官方 `/v2`，Google Gemini 保持官方 `/v1beta` 原生路径。

### 8.1 路由

| 协议 | 方法 | 路由 | 请求形态 |
| --- | --- | --- | --- |
| OpenAI | GET | `/v1/models` | JSON |
| Codex | GET | `/v1/codex/model_catalog` | Codex 模型目录 JSON |
| OpenAI | POST | `/v1/chat/completions` | JSON，支持 `stream=true` SSE |
| OpenAI | POST | `/v1/completions` | JSON，支持 `stream=true` SSE |
| OpenAI | POST/WS | `/v1/responses` | JSON SSE 或 Responses WebSocket；Codex CLI `response.create` 原样透传并改写 model |
| OpenAI | POST | `/v1/responses/compact` | JSON |
| OpenAI | POST | `/v1/embeddings` | JSON |
| Cohere Rerank | POST | `/v2/rerank` | JSON |
| OpenAI | POST | `/v1/images/generations` | JSON |
| OpenAI | POST | `/v1/images/edits` | multipart/form-data |
| OpenAI | POST | `/v1/images/variations` | multipart/form-data |
| OpenAI | POST | `/v1/audio/transcriptions` | multipart/form-data |
| OpenAI | POST | `/v1/audio/translations` | multipart/form-data |
| OpenAI | POST | `/v1/audio/speech` | JSON，响应音频流 |
| OpenAI | POST | `/v1/videos` | JSON 或 multipart/form-data；返回异步 Video resource |
| OpenAI | GET | `/v1/videos/{video_id}` | JSON |
| OpenAI | GET | `/v1/videos/{video_id}/content` | 视频二进制流 |
| OpenAI | POST | `/v1/videos/{video_id}/remix` | JSON |
| OpenAI | POST | `/v1/moderations` | JSON |
| Anthropic | POST | `/v1/messages` | JSON，支持 `stream=true` SSE |
| Google Gemini | POST | `/v1beta/models/{model}:generateContent` | JSON |
| Google Gemini | POST | `/v1beta/models/{model}:streamGenerateContent` | JSON；`alt=sse` 时为 SSE |
| Google Gemini | POST | `/v1beta/models/{model}:embedContent` | JSON |
| Google Gemini | POST | `/v1beta/models/{model}:batchEmbedContents` | JSON |
| OpenAI Realtime | WS | `/v1/realtime` | WebSocket 双向事件 |

不提供旧 `/api/nebula/gateway/*`、`/models`、`/chat/completions`、`/messages` 等别名，也不提供 health、usage、monitor 或 provider internal 路由作为公共 API。

协议实现依据：

- OpenAI Responses：[Create a model response](https://developers.openai.com/api/reference/resources/responses/methods/create/) 与 [Compact a response](https://developers.openai.com/api/reference/resources/responses/methods/compact/)
- OpenAI Embeddings：[Create embeddings](https://developers.openai.com/api/reference/resources/embeddings/methods/create/)
- OpenAI Images：[Images API](https://developers.openai.com/api/reference/resources/images/)
- OpenAI Audio：[Audio and speech](https://developers.openai.com/api/docs/guides/audio)
- OpenAI Videos：[Videos API](https://developers.openai.com/api/reference/resources/videos/)
- OpenAI Realtime：[Realtime and audio](https://developers.openai.com/api/docs/guides/realtime) 与 [WebRTC](https://developers.openai.com/api/docs/guides/realtime-webrtc)
- OpenAI Moderations：[Moderation](https://developers.openai.com/api/docs/guides/moderation)
- Anthropic Messages：[Messages API](https://docs.anthropic.com/en/api/messages)
- Cohere Rerank v2：[Rerank API](https://docs.cohere.com/reference/rerank)
- Google Gemini：[Generating content](https://ai.google.dev/api/generate-content)、[Embeddings](https://ai.google.dev/api/embeddings) 与 [All methods](https://ai.google.dev/api/all-methods)

### 8.2 鉴权与授权

- OpenAI-compatible HTTP 使用 `Authorization: Bearer <nebula_api_key>`。
- Anthropic Messages 同时接受协议原生 `x-api-key: <nebula_api_key>`；要求并透传合法 `anthropic-version`。
- Google Gemini 原生路由接受 `x-goog-api-key: <nebula_api_key>` 或 `Authorization: Bearer <nebula_api_key>`；禁止使用 `?key=`，网关向上游覆盖为供应商凭据。
- Realtime 使用 `Authorization` Header；浏览器无法设置 Header 时可使用标准 `Sec-WebSocket-Protocol` 承载凭据，不接受查询参数中的 API Key。
- Responses WebSocket 使用同一个 `/v1/responses`，在首个 `response.create` frame 中提取公开 model 并改写为上游 model；`OpenAI-Beta`、Codex metadata Header、`previous_response_id`、`prompt_cache_key`、加密 reasoning/compaction item 和未知字段均透明保留。
- Key 必须为 `active`，所请求 `model` 必须位于 `api_key_models` 白名单，模型、binding 和 provider 均须为 `active`。
- `/v1/models` 和 `/v1/codex/model_catalog` 请求时读取当前 Key 授权，并返回 `Cache-Control: no-store`。默认保留 `available` 和 `unverified`，加 `?include_unavailable=1` 可查询已授权的 `maintenance` 模型。`/v1/models` 包含已授权的 Claude 模型并返回完整协议与状态字段。Codex 目录沿用其客户端格式和模型族筛选。

网关按模型倍率执行月度 credit 额度检查与调用记录；目录查询、视频状态查询和内容下载不计费。

受理时按首选路由检查，派发前再按实际路由检查。默认仅北辰路由检查待核金额，同学生跨 Key、跨项目共用额度。待核积分合计加本笔默认预留达到上限时返回 HTTP 429，响应头 `X-Nebula-Error-Code` 为 `unresolved_exposure_limit`。所有路由都检查并发，已达并发限制时优先返回 429 `student_concurrency_exceeded`。

| 后端环境变量 | 默认值 | 含义 |
| --- | ---: | --- |
| `NEBULA_UNRESOLVED_EXPOSURE` | 1 | 设为 `0` 整体回退，所有路由恢复按学生待核笔数准入，达到上限返回 429 `pending_review_limit`，关闭推荐分项自动结算 |
| `NEBULA_STUDENT_PENDING_REVIEW_LIMIT` | 20 | 整体回退模式的待核笔数上限 |
| `NEBULA_STUDENT_UNRESOLVED_CREDIT_LIMIT` | 1000 | 每学生待核预留上限，整积分 |
| `NEBULA_STUDENT_PENDING_RESERVE_CREDITS` | 10 | 每笔新待核调用预留的整积分，折合 0.1 元 |
| `NEBULA_STUDENT_CONCURRENCY_LIMIT` | 5 | 同一学生的所有 Key 和项目共用。直接运行和本地 Compose 可用环境变量覆盖；生产 Compose 显式指定 5，优先于服务器 `.env` |

笔数、额度和并发数接受正整数，缺省或无效时使用默认值。按默认值，已有 98 笔各 10 积分时，第 99 笔可发出。已有 99 笔各 10 积分时，下一笔北辰请求正好触顶，须等部分记录完成核账后再试。

`NEBULA_PENDING_AUTO_SETTLE` 默认 `1`，单独设为 `0` 仍计算待核预留并执行金额准入，仅关闭推荐分项自动扣款。支持的纯文本协议为 OpenAI Chat、Responses 和 Anthropic Messages。Gemini 原生响应缺少缓存写入明细，仍待核，不自动结算。`NEBULA_PENDING_AUTO_SETTLE_YUAN`（默认 `0.001` 上游元）和 `NEBULA_PENDING_AUTO_SETTLE_RELATIVE`（默认 `0.01`，即 1%）可设为非负有限数，无效值使用默认值。区间宽度满足任一阈值或金额唯一时，按推荐分项交给原定价流程。成功后 `pricing_detail.auto_settled=true`，另存上下界、推荐金额、历史比例和策略版本。

`MaxYuan` 已含北辰分组倍率。学生上界为 `MaxYuan × 100 × 学校倍率`，缺学校倍率按 2 换算。同一基础报价 0.250012 元，学校倍率 1.10，分组倍率 1、2 分别预留 28、56 整积分。已知上界向上取整并限制在 1 至 1000，首次未知上界取默认预留，两者均以学生剩余额度为限（可为 0）。候选扣款低于或等于未取整上界时按原值扣款。高出不超过 `0.000000001` 积分时只扣到上界，高出更多则继续待核。原有教师定价能确定唯一金额时优先入账，例如 `estimated_unique_amount` 的 0.03602500007040 积分沿用教师价。

老师模型 Probe 支持 `chat_completions`、`responses`、`messages`、`embeddings`、`rerank`。每次请求固定先调用同一供应商的 `GET /v1/models`，按大小写不敏感的完整上游模型 ID 精确匹配目录条目。匹配条目的纯文本 `description` 直接写入建议配置的描述；`model_type`、`tags`、`supported_endpoint_types` 与 description 共同映射类别、输入/输出模态和固定能力。容量采用固定优先级：响应中明确命名的结构化字段最高，其次从 description 的“100 万令牌”“1M context”“128K 上下文”“最大输出 16K”等格式提取，再按具体模型族推断，最后使用 128K 上下文、128K 最大输入和 4K 最大输出。更具体的模型族规则先于通用规则，例如 `claude-sonnet-4-5` 不会误匹配 Claude 5，`glm-5` 使用 1000K/16K。之后按请求顺序执行所选真实端点。浏览器响应不包含上游响应 body，只返回各请求的 endpoint、path、HTTP 状态、耗时、截断标志、通用错误和标准化配置。已注册供应商只提交 `provider_id`，凭据不返回浏览器；手写 Key 不持久化。手写 Base URL 只允许公网 HTTP/HTTPS，不跟随重定向，并在连接阶段再次拒绝私网、localhost、链路本地和云元数据地址。单项上游响应最多读取 64 KiB；不把 usage、价格、Content-Length 或 `max_bytes` 当成容量。

```json
{
  "results": [
    {"endpoint":"models","path":"/v1/models","http_status":200,"duration_ms":31,"truncated":false,"limits":{"context_window":128000}},
    {"endpoint":"responses","path":"/v1/responses","http_status":200,"duration_ms":804,"truncated":false,"limits":{}}
  ],
  "configuration": {
    "description":"Model description",
    "category":"text",
    "capabilities":["reasoning","tool_calling"],
    "input_modalities":["text"],
    "output_modalities":["text"],
    "context_window":128000,
    "max_input_tokens":128000,
    "max_output_tokens":8192
  }
}
```

控制台顶部四节点是唯一的步骤导航，第一节点不提供“下一步”按钮；模型 ID 和名称完整后“基础配置”节点解锁。进入第二节点时先写入默认配置：描述使用显示名称与 model ID，类别为 `text`，输入/输出模态为 `text`，能力为空，上下文和最大输入为 `128K`，最大输出为 `4K`，常用模型为否，倍率为 `1.00x`。随后用 `/v1/models` 的真实 description、确定性元数据映射和 Probe 明确返回的字段覆盖默认值；测试完成自动关闭一键配置弹窗并回到第二节点，不展示上游响应正文。Binding 新增和编辑均使用 Portal 居中弹窗，不使用页面内固定定位面板。

### 8.3 代理与故障切换

- 保持协议原生 request/response、状态码、SSE 事件、multipart 文件和 WebSocket frame，不使用控制面 envelope。每类协议只选择同协议 adapter，不做跨协议 DTO 转换。
- `/v1/responses/compact` 是独立无状态 compact：网关不得裁剪返回 `output`，也不得解析或替换 opaque `encrypted_content`；Codex CLI 使用的 tools、reasoning、text、prompt cache 与 service tier 字段仅改写顶层 model。
- Responses WebSocket 的每个 `response.create` 单独记账。北辰按对应响应的终止用量结算，同一连接同时只接受一个正在生成的请求。北辰 Realtime 以连接为单位记录待核账，服务端触发的多次生成不按纯文本单价扣积分。其他供应商的 Realtime 仍按 `response.create` 计费。
- Videos 创建成功后，Redis 保存 `video_id -> binding_id`；查询、内容下载和 remix 必须回到创建该资源的上游，不能按 priority 重新选择供应商。
- OpenAI 和 Cohere 请求体中的公共 `model` 会替换为 Binding 的上游模型名；Gemini 的路径模型会替换为上游模型名，`batchEmbedContents.requests[*].model` 同步改写为 `models/{upstream_model_name}`。
- 候选 binding 按 `priority ASC, id ASC` 排序。
- 仅在连接错误、连接/响应超时、HTTP `429` 或 `5xx` 时尝试下一 binding。
- `4xx` 参数或鉴权错误除 `429` 外不得跨供应商重试。
- 流式响应在任何字节已发给客户端后不得切换供应商，以免拼接两个上游响应。
- 所有候选失败时，以目标协议的原生错误结构返回最后一个可解释错误，并附服务端 `request_id` 响应 Header；不得泄露供应商凭据或内部 URL。

### 8.4 协议错误

OpenAI-compatible 示例：

```json
{
  "error": {
    "message": "The requested model is not allowed for this API key.",
    "type": "invalid_request_error",
    "param": "model",
    "code": "model_not_allowed"
  }
}
```

Anthropic-compatible 示例：

```json
{
  "type": "error",
  "error": {
    "type": "permission_error",
    "message": "The requested model is not allowed for this API key."
  }
}
```

## 9. 用量与监控接口

学生使用 `GET /api/v1/student/usage?month=YYYY-MM` 查看每个 API Key 的模型 credit 分解。

学生使用 `GET /api/v1/student/usage/calls/{request_id}` 查自己的一笔北辰消费。请求编号取自学校网关响应头 `X-Request-ID`。金额以十进制字符串返回，缺失值为 `null`。

| 字段 | 含义 |
| --- | --- |
| `base_credits`、`base_source` | 基础积分及依据。来源标记区分网关用量与报价、官方用量与报价、按官方实扣折算和历史账目。 |
| `school_multiplier`、`charged_credits` | 请求发出时确定的学校系数和学生实际用掉的积分。由学校接管的四条北辰渠道在新请求上使用 1.10。其他凭据及历史派发保留原系数。 |
| `actual_cost`、`receipt_applied` | 能关联的北辰原始实扣，以及本次是否依据回执完成补记。1 个北辰余额单位折合 1 元人民币或 100 个基础积分。 |
| `request_id`、`model_id`、`channel`、`billing_state` | 请求归属和当前结算状态。 |

教师使用 `PATCH /api/v1/teacher/projects/{project_id}/members/{user_id}/monthly-credit-quota` 调整学生的项目月额度。请求必填积分字符串 `monthly_credits`，可选 `quota_change_reason` 记录原因，成功返回 HTTP 204。学生用完当月个人额度后，新网关请求返回 `member_quota_exceeded`。已发出的请求按实际用量结算。

项目用量的 `members[].pending` 返回月桶中仍预留、尚未结算的积分十进制字符串（`pending_milli × 0.001`），与 `pending_review` 布尔状态区分；教师调整额度时可连同已用金额计算可下调的下限。

匿名返回 401，其他身份返回 403。在本人范围内找不到唯一、可读的北辰调用时统一返回 404，包含本人请求编号重用、他人的调用和其他供应商的调用。

导师使用 `GET /api/v1/mentor/projects/{project_id}/usage` 查看项目额度分配、成员 Key 进度与实际开销；`GET /api/v1/mentor/call-logs` 和 `GET /api/v1/mentor/input-monitor` 使用游标分页与项目范围授权。完整输入仅在详情读取时返回并写访问审计。

导师可通过 `PATCH /api/v1/mentor/api-keys/{api_key_id}/monthly-credit-quota` 调整负责项目内 Key 的月额度，请求包含 `monthly_credits` 与必填 `reason`。

老师使用 `GET /api/v1/teacher/project-spend` 查看按模型聚合的项目花费和 0 倍率模型调用次数。额度拒绝返回 429 以及 `api_key_quota_exceeded` 或 `project_quota_exceeded`。

## 10. 明确排除项

- 不实现余额、RPM、Token 限制、通知中心或模型价格。
- 不提供老师浏览项目成员或 ACTIVE Key 的接口。
- 不提供资源删除接口。
- 不代理 Files、Uploads、Batches、Fine-tuning 或 Assistants/Threads 等有状态资源 API；这些接口没有可在每个请求中稳定取得的公开 model，若未来纳入必须新增资源 ID 到 Binding 的完整归属状态与授权契约，不能按默认 Binding 猜测上游。
- 不提供旧路由兼容层、别名、双写或 internal usage/monitor 路由。

## 10. 官方 Web 控制台消费约定

仓库内 `frontend/` 是上述契约的官方 Vue 客户端。Vercel 项目的 Root Directory 为 `frontend`，生产页面使用 `https://www.lyn91r.cn`，并通过 `VITE_API_BASE_URL=https://api.lyn91r.cn` 调用 API；`frontend/vercel.json` 将 Vue Router history 路由统一回退到 `/index.html`，因此 `/login`、`/teacher/...` 等前端路由支持直接访问和刷新。本地 Vite 仍使用 `/api` 与 `/v1` 代理。生产 API 由 Cloudflare Tunnel 暴露为 `https://api.lyn91r.cn`，Tunnel Service 指向内部 `http://backend:8080`；cloudflared 通过生产 `edge` 网络直接访问 backend 并连接 Cloudflare edge，不改变公开路由或 HTTP 契约。控制面 CORS 只允许正式前端 Origin，并允许 credentials、Authorization、Content-Type 和 Last-Event-ID；Realtime WebSocket 同样校验正式前端 Origin。

- access token 只保存在 Pinia 内存中，不写入 localStorage、sessionStorage 或 URL。
- refresh token 由 HttpOnly Cookie 管理，所有请求使用 `credentials: include`；并发 `401` 共享一次 refresh 请求，失败后返回登录页。
- 页面启动时路由守卫与应用壳共享同一个 bootstrap refresh 请求；不会因重复调用轮换同一个 refresh token。刷新成功后重新建立内存 access token 和当前用户，刷新失败才进入登录态失效流程。
- 当前用户与角色以 `/api/v1/me`/session 响应为准，路由守卫不能替代服务端权限校验。
- 工作区路由使用全局唯一的技术名称，中文页面标题存放在独立的路由元数据中；导师与老师均可使用“项目管理”标题，但 `/mentor/projects` 与 `/teacher/projects` 必须同时保留独立路由记录。
- 官方 API client 对控制面 GET 与写请求维护仅存在于内存的活动计数：GET 活动只驱动全局不占布局的顶部进度条，学生、导师、老师页面及候选弹窗通过局部 `LoadingRegion` 在自身内容区域显示骨架；刷新保留已渲染数据，写请求显示提交状态并由具体操作按钮阻止重复提交。计数必须在成功、结构化错误、网络错误和 `401` refresh 重试后清零；该 UI 状态不改变请求、响应 envelope、鉴权或错误处理契约。
- 页面切换、弹层、状态和加载反馈遵循 `prefers-reduced-motion`；关闭位移、缩放、旋转和 shimmer 后仍必须保留文字、禁用态与 `aria-busy` 等可访问状态。
- 老师模型卡片将 `status=pending_configuration` 且 `route_ready=true` 的模型展示为“已就绪”；`route_ready` 只作为权威派生条件，不再重复渲染为卡片小泡泡。该展示映射不修改 API 返回的模型状态，启用/停用仍遵循模型状态契约。
- `/api/v1/events` 使用带 `Authorization` Header 的 fetch streaming，不使用原生 EventSource；收到状态事件后重新获取 REST 权威数据。
- 一次性 API Key 仅在领取弹窗中显示和复制，关闭后不写入浏览器持久存储，也不支持再次展示。
- 老师控制台不提供项目成员和 ACTIVE Key 浏览入口，前端不得扩大服务端可见范围。
