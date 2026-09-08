# ADR 001: 项目额度与角色分工

## 状态

已采纳（2026-09-01）

## 背景

平台以项目为中心运作。老师负责平台治理与项目月额度；导师负责项目内 Key 终审与个人额度；组织仅为项目附属字段，不设组织级额度。

## 决策

### 两种额度

| 类型 | 存储 | 写入角色 |
|------|------|----------|
| ProjectBudget | `projects.monthly_credit_quota_milli` | 老师 |
| KeyAllocation | `api_keys.monthly_credit_quota_milli` | 导师 |

二者共用 `project_month_credit_buckets` / `api_key_month_credit_buckets` 计量。

### 三个界面

1. **项目额度** `/teacher/project-budgets` — 老师维护全项目月额度
2. **Key 运维** — 导师审核 Key、调额度、撤销（原老师终审职责）
3. **项目监控** `/teacher/project-monitor` — 老师只读观测，DTO 与导师项目用量一致

### Key 状态机 v2

```
pending_mentor → (导师 approve + FinalizeKeyApproval) → approved → claim → active
```

移除 `pending_teacher` 主路径；存量数据迁移回 `pending_mentor` 由导师重新终审。

### 访问控制

- `internal/access`：`CanAccessProject(actor, scope)` — 老师只读全项目，导师读写所属项目
- 平台治理（供应商/模型/组织）仅老师

## 后果

- 删除老师 Key 终审 API 与页面
- `TeacherProjects` 不再编辑额度
- 导师审核通过时直接调用 `ApproveKey`（quota 校验 + bucket 分配）
