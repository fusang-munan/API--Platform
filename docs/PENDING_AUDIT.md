# 待核账只读审计

`cmd/pending-audit` 对每笔待核账记录输出上游金额区间、推荐的缓存读写量和自动结算判定。该命令通过只读事务生成报告。

## 运行

```bash
DATABASE_URL='postgres://只读用户:密码@主机:5432/数据库?sslmode=require' \
  go run ./cmd/pending-audit -limit 2000 -out pending-audit.json
```

可选参数：

- `-threshold 0.001` 设置自动结算允许的最大区间宽度，单位为元。
- `-relative 0.01` 设置自动结算允许的最大相对区间宽度，0.01 表示 1%。
- `-prior-read-share 0.5` 设置缓存读缺失时的推荐比例，取值为 0 到 1。
- `-limit 2000` 设置最多读取的待核账笔数。

建议用只读账号，连接串加 `default_transaction_read_only=on`；程序本身也在只读事务里查询。

## 计数和金额口径

- Anthropic 的总量等于普通输入、输出、缓存读和缓存写之和。总量可反推唯一缺失的缓存项。总量缺失时，缓存读和写均须已知。
- OpenAI Chat、Responses 和 Gemini 的总量等于输入与输出之和。输入包含普通输入、缓存读和缓存写。已知缓存量先从输入里扣除，剩余量才用于拆分。
- 区间两端把剩余 Token 全分给可接收它们的最便宜或最贵分项。所有可能承载正数 Token 的分项都需要有效单价。
- 推荐值保留已知缓存量。缺失的缓存读按推荐比例乘剩余量，四舍五入到整数。余量落到其余可分配的分项。普通输入和缓存写均可分配时，两者平分余量，奇数多出的 1 个 Token 归普通输入。只有一个分项可吸纳剩余量时，该分项取全部余量。

## 分类和 JSON 输出

- `exact` 表示金额唯一，包括可以反推分项、可吸纳余量的分项费率相同等情况。
- `narrow` 表示绝对或相对区间宽度满足对应阈值，等于阈值也通过。
- `wide` 表示区间超过两个阈值。
- `unknown` 表示证据不足或矛盾，或调用协议缺少适用的纯文本计费口径。检查 `missing` 和 `note`。该分类的金额和推荐 Token 字段以零占位。

`can_auto_settle` 对 `exact` 和 `narrow` 为真。`min_yuan`、`max_yuan` 和 `recommended_yuan` 分别给出下界、上界和推荐金额。`recommended_read` 和 `recommended_write` 给出推荐拆分的最终缓存量。`estimated_yuan` 与推荐金额一致。`unknown_tokens` 是从缺项推导或分配的 Token 余量。

`max_error_yuan` 是上界减下界。`relative_error` 是区间宽度除以下界。下界为零且区间宽度为正时，相对阈值无法通过，报告用浮点上限表示无有限相对误差。

1 个上游单位按 1 元计。区间以调用方提供的四项单价为依据，学生倍率由结算层应用。命令查询派发时刻的基础报价，将每百万 Token 的百万分之一余额单位换算成元／Token。分档价、额外计价项和回执仍需核对。
