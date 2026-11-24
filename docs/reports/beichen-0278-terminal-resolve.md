# 北辰 0.27.8：缺终态自动收口返工

## 背景

0.27.7 上线后，生产仍有 15 笔调用停在 pending_review，原因全部是 missing_terminal_usage：客户端断开、超时或流中断后，上游没有返回终态。0.27.8 的目标是超过宽限期后按已知用量结清，未知输出按 0，没有已知计费用量或无法定价时按 0 核销并留痕，避免待核无限堆积。

独立复核在首版实现上判了 FAIL，发现四类问题：

1. 响应模型与派发模型不一致、且响应模型没有报价时，结算器仍按派发模型扣了 0.00462 积分。
2. 缺终态同时带缓存分项冲突时，冲突证据被当成零元核销，记录离开了人工待核队列。
3. 只上报 cache_creation 的 5 分钟子项（4 tokens）时，已保存的已知缓存写入被记成 0。
4. 批量上限设为 1 时，一笔持续失败的最老记录会反复占用唯一名额，后续正常记录一直无法结清。

## 修复

- 网关保留响应自身证明的计费审核原因。模型不符、未确认模型、不支持的文本定价和不完整分项优先于“流中断”，不再被覆盖成 missing_terminal_usage。
- 缺终态扫描排除解析冲突：invalid_usage_payload、invalid_token_count、conflicting_usage_fields、conflicting_cache_creation_counts、inconsistent_token_total、inconsistent_token_breakdown。这些记录保留人工待核，不由缺终态策略按 0 核销。
- 教师端 terminal_missing 计数使用同一可处理集合；冲突记录计入总待核并显示“缓存或计数证据冲突，保留人工待核”，不再出现“显示 15 笔、实际只处理 14 笔”。
- 结算时回读 pricing_detail 中的 cache_write_5m_tokens 和 cache_write_1h_tokens。父字段缺失但子项有直接观测时，按子项计入已知输入；父字段存在时以父字段为准。
- 输入完整性按数据库中的原始证据判断。协议要求的零值补位不算“已知输入”，避免在分档价格下误用更便宜的档位。
- 扫描读取更宽的候选窗口，只有成功结清才计入批次上限。单笔事务失败会回滚并保留原记录，其他候选继续处理。

## 结算口径

- 只处理 beichen_review_reason='missing_terminal_usage'。
- 已知输入和输出按派发时的价格快照计价；未知输出按 0。
- 没有已知计费用量、缺少派发价格或无法可靠定价时按 0 积分核销，并在 pricing_detail 写入 terminal_missing、terminal_policy、known_input_tokens、known_output_tokens、priced、reason 和 processed_at。
- 已有扣款不回改；迟到输出不会二次扣款。
- 该策略可能少收，以确保未知用量绝不多扣。缓存或计数证据冲突不自动核销，留给教师人工判断。

## 验收

- 复核人原始独立用例：15 笔真实网关样本 9 笔正额、6 笔零元，逐笔不超过已知用量上界，三种月度账和明细合计 0.0495；并发、宽限、hold、批量、幂等、迟到输出、教师权限、前端 353 项均通过。
- 新增 PostgreSQL 反例：模型不符保持 upstream_model_mismatch 且不扣款；缓存冲突保持 pending_review；5 分钟子项 4 tokens 结清 0.0011；批量 1 时后续正常记录仍能结清。
- 定向回归：go build、go vet、internal/usage、internal/dataplane、internal/beichen、internal/api/http、cmd/server 全部通过。
- 前端：typecheck 通过，47 个文件 353 项测试通过。

## 残余风险

- 模型不符只在网关看到上游响应时识别。0.27.7 期间已经落库的历史行没有保存响应模型；如果其中存在模型替换，缺终态结算仍会按派发模型的报价计算。生产 15 笔为正常测试流量，暂无证据表明存在模型替换。
- 解析冲突记录会一直保留在人工待核队列，直到教师处理；这是有意的安全行为，不是自动清账失败。
- 候选窗口最多 500 笔。若连续 400 笔以上都发生数据库级失败，后续记录仍可能等待下一轮扫描；单笔失败不会再阻塞整批。
- 真实上游账单与生产浏览器逐页点选不在本机验收范围内。
