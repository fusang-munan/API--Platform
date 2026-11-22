# 北辰 0.26.1 热修：上游 4xx 不再占用待核额度

## 现象

DSH 使用 Claude High 时收到上游 400（adaptive thinking），随后调用 GPT 也收到 429：

```text
429 {"code":"pending_review_limit","message":"Too many model calls are awaiting billing review for this student."}
```

学生用量页显示，400 的调用没有 Token、扣分为 0，却被计入待核账。

## 根因

网关对已派发但上游返回确定性 4xx 的调用，沿用了未知结果路径，写入
`beichen_state=pending_review`。默认每名学生 20 笔待核就会触发
`pending_review_limit`，与模型、协议和余额无关。

## 修复

- 最终上游 HTTP 状态写入 `CallRoute.UpstreamStatus`。
- 上游 400/401/403/404/429 等确定性 4xx（408 除外）在派发后直接释放，不再进入待核；无 Token、无扣分、无 hold。
- 迁移 `0063_release_upstream_4xx_reviews.sql` 清理历史遗留的同类待核行：仅释放
  `error_category='http'`、`HTTP 4xx`、`credit_milli=0`、无扣分的记录。

## 验收

- 上游 400 的账本状态为 `released`，不占待核额度。
- 全部 Go 测试（含隔离 PostgreSQL）通过。
- 部署后学生待核计数下降，GPT/DeepSeek 不再被 Claude 的 400 连带 429。
