# FRP 公网暴露说明

本机 Nebula-api 通过 `frpc` 把本地端口映射到公网服务器 `106.55.103.208`。

## 标准流程（与 MegaFlowAI 其他项目一致）

1. **公网侧 `frps`** 已在 `106.55.103.208:7000` 运行，使用 token 认证。
2. **内网侧 `frpc`** 在本机 `/data/home/lab_proj/frp_0.62.0_linux_amd64/` 运行，读取 `frpc.toml`。
3. 在 `frpc.toml` 增加 `[[proxies]]` 块：`type = "tcp"`，`localIP = "127.0.0.1"`，`localPort` 指向本机前端（25555），`remotePort` 为公网端口（25555）。
4. 执行 `./frpc.sh restart` 重载配置。
5. 在公网服务器防火墙/安全组放行对应 `remotePort`。
6. 用 `curl http://106.55.103.208:25555/` 验证。

后端本机调试端口为 `127.0.0.1:25556`（与 25555 对齐，不映射公网）。

官方推荐格式为 TOML（frp v0.52.0+），详见 [VictoryReport FRP 说明](../VictoryReport/VictoryReportBackend/FRP_FIRECRAWL_SETUP.md)。

## 本项目映射

| 代理名 | 本机 | 公网 | 用途 |
|--------|------|------|------|
| MegaFlowAI-api-platform | 127.0.0.1:25555 | 106.55.103.208:25555 | 前端控制台（Nginx 同源代理 `/api`、`/v1`） |

**公网入口**：`http://106.55.103.208:25555`

0.10.2 的维护 API 使用 `https://106.55.103.208:25555`。同一端口的明文请求继续进入原控制台，TLS 字节由 Nginx 转交后端处理。后端仅在实验室 Compose 启用维护监听，复用教师和学生的现有鉴权。维护客户端使用独立的 Cookie 容器并验证学校证书，同时校验 IP 和有效期。

学校实例首次启动时自行生成证书，私钥只由后端进程读写并保存在独立命名卷。管理员在学校主机执行以下命令可取得本次使用的公开证书摘要。通过维护者认识的校方联系人或经核实的正式发件地址传递完整 SHA-256。客户端必须先把从网络取得的叶证书与此值逐字节核对，再用这张证书作为本次连接的信任锚。浏览器和系统的全局信任设置无需修改。

```bash
bash scripts/compose-local.sh logs --no-color --tail=300 backend | grep 'maintenance HTTPS certificate' | tail -1
```

若日志显示 maintenance HTTPS unavailable，先检查命名卷的权限及证书有效期。保留现有卷，按学校批准的流程轮换证书并重新分发指纹。

维护入口的 `GET /.well-known/nebula-maintenance` 供无凭据的上线检查使用。`email_delivery` 为 `log_only` 表示该实例只写开发日志，为 `smtp_configured` 表示已选择 SMTP，实际投递需要收件方确认。此接口不发信。

## 维护命令

```bash
# 重载 frpc（修改 frpc.toml 后）
/data/home/lab_proj/frp_0.62.0_linux_amd64/frpc.sh restart

# 查看 frpc 状态
/data/home/lab_proj/frp_0.62.0_linux_amd64/frpc.sh status

# 验证公网
curl -sS http://106.55.103.208:25555/
```

`frpc.sh` 启动时会清掉 shell 代理变量，直连 `106.55.103.208:7000`，与 ~/.profile 里的 7890 无关。

## 环境变量

本地 `.env.local` 中 `PUBLIC_APP_URL` 应设为公网前端地址，以便登录回调与 Cookie 域一致：

```env
PUBLIC_APP_URL=http://106.55.103.208:25555
```

修改后需重建 backend：

```bash
cd /data/home/lab_proj/MegaFlowAI/api-platform
bash scripts/compose-local.sh up -d --force-recreate backend
```

## 新增项目时的端口约定

MegaFlowAI 项目在 `frpc.toml` 中统一使用 `MegaFlowAI-<项目名>` 命名。api-platform 公网端口为 **25555**。
