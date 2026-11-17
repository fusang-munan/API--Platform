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

可选 HTTPS（自签，多机访问不推荐）：见 `scripts/generate-lab-tls.sh` 与 `frontend/nginx.lab-ssl.conf`，在 `compose.local.yaml` 为 frontend 挂载证书与 SSL 配置。

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
