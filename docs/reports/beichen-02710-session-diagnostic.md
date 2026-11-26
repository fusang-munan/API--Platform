# 0.27.10 北辰网页会话加载诊断与兜底

## 背景

0.27.9 已包含重新抓取的 school-session.sealed.json，但服务器启动后 web_session_configured 仍为 false，目录抓取继续提示“请先配置北辰网页账号或 Token”。

## 改动

- schoolseal.SessionReleaseDigest() 暴露嵌入式会话文件的 SHA-256，便于确认服务器实际加载的是哪一版密文。
- PanelAccountStatus 增加 web_session_configured、web_session_error、session_release_sha256，不改变原有字段。
- 启动配置失败时把错误写入账户状态，不再只留在服务器日志。
- 续期缓存损坏时回退到新交付的会话；交付的密文仍是唯一可信来源。

## 验收

- 新增损坏续期缓存的回退测试；全量 go test ./... 与 go vet ./... 通过。
- 部署后对比 session_release_sha256 与抓取文件哈希，确认构建确实包含新会话。

## 边界

- 不改变计费口径；只增加诊断和缓存兜底。
- 若嵌入式密文本身无法解密，web_session_error 会给出具体层级的错误，仍需服务器私钥与密文匹配。
