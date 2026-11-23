# 北辰 0.27.4：修复 Anthropic 顶层 system 被上游丢弃

## 问题

2026-11-23 上线后测试发现，发给北辰 `/v1/messages` 的标准顶层 `system` 字段没有到达模型：

- 带约 2,800 Token 长 system、用户只说一句话的请求，上游只回报 11 个输入 Token，缓存读写为 0。
- 同样内容放进普通用户消息，输入 110、缓存写 2,649；放进 `messages` 里的 `role=system` 消息，缓存写 2,803；用 Chat 协议的系统消息，输入 2,807、缓存写 2,803。

结论是北辰的 Anthropic 端点忽略顶层 `system`，但识别 `messages` 里的 `role=system`。Claude Code 等客户端把系统提示放在顶层 `system`，导致指令被静默丢弃、模型行为变差，这部分 Token 也没有进入账单。Chat 的系统消息和 Responses 的 `instructions` 经实测正常。

## 改动

仅在「Anthropic 适配器 + 北辰上游 + `/v1/messages`」时，把顶层 `system` 提升为 `messages` 首条 `role=system` 消息：

- 字符串 system 转成一个 `type=text` 的文本块。
- 块数组 system 原样作为 content，保留 `cache_control` 和 TTL。
- 已有首条 system 消息时不重复插入。
- 非北辰 Anthropic 上游保持标准请求形状。

系统提示现在会真正生效，并按上游实际回报计入普通输入、缓存写和缓存读。学生扣费公式不变，仍按上游用量和当前价格结算。

## 验证

- 新增单元测试：字符串提升、块数组与 cache_control 保留、已有 system 不重复、北辰与其他上游分流。
- `go test -p 1 ./...` 全量通过；`go build ./...`、`go vet ./...` 通过。
- 上线后用同一测试 Key 复测：长 system 的 Anthropic 请求应出现约 2.8k 缓存写 Token，而不是 11 个输入。

## 残余风险

- 若北辰未来修复顶层 `system`，提升后的 `role=system` 消息仍可被识别；若其拒绝该形状，需要回退此适配。
- 同时携带顶层 `system` 和首条 system 消息的请求不做合并，保持原样。
- 本轮只处理 Anthropic 协议；Chat 系统消息和 Responses `instructions` 已实测正常。
