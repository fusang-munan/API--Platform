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

**学校 HTTPS 控制台**为 `https://106.55.103.208:25555`。浏览器通过证书检查后，从 `/teacher/providers` 进入供应商管理，从 `/student/api-keys` 进入学生密钥申请。明文入口可用于匿名读取发布版本。

0.10.3 的学校 Nginx 启动配置随镜像分发。镜像构建完毕后，重建前端容器不依赖服务器上检出源码的目录和权限。公网可先执行 `curl --noproxy '*' -fsS http://106.55.103.208:25555/.well-known/nebula-release.txt` 看镜像版本。响应正文应为一行版本号。旧页面的 HTML 也会返回 HTTP 200，请同时看正文。

从 0.13.0 起，Nginx 将同端口的 TLS 流量交给学校后端。后端直接处理登录、审批、供应商配置及网关 API，仅将网页和构建资产的读取交给前端容器内的 `frontend:8082`。该端口只提供静态文件，未发布到宿主机。浏览器的凭据、请求正文和查询参数不会转发给静态文件服务。打开 HTTPS 后应通过该入口重新登录。

学校实例首次启动时自行生成证书，私钥只由后端进程读写并保存在独立命名卷。管理员在学校主机执行以下命令可取得正在使用的服务器证书摘要。可通过经核实的学校邮件独立传递完整 SHA-256。专用维护客户端支持按指纹信任单张服务器证书，并校验 IP 和有效期。普通浏览器使用自身的证书信任机制；若出现未知签发机构，请在获信任的入口登录。发布网页本身不会更改学生设备的信任设置。

```bash
bash scripts/compose-local.sh logs --no-color --tail=300 backend | grep 'maintenance HTTPS certificate' | tail -1
```

若日志显示 maintenance HTTPS unavailable，先检查命名卷的权限及证书有效期。保留现有卷，按学校批准的流程轮换证书并重新分发指纹。

维护入口的 `GET /.well-known/nebula-maintenance` 供无凭据的上线检查使用。`email_delivery` 为 `log_only` 表示该实例只写开发日志，为 `smtp_configured` 表示已选择 SMTP，实际投递需要收件方确认。此接口不发信。

### 校方北辰工作区入口

校方给维护教师开通远程应用并在该应用中限定访问者后，将教师的中转站用户 UUID 填入 `LAB_BEICHEN_WORKSPACE_TEACHER_ID`，将受管应用稳定的 HTTPS 地址填入 `LAB_BEICHEN_WORKSPACE_URL`。这两项由学校部署配置读取。工作区使用独立主机名，以隔离中转站登录 Cookie。地址应直接指向工作区，使用不含用户名、密码、查询参数和片段的长期入口。

同一教师登录已核实的学校 HTTPS 站点后，在「供应商管理 → 北辰 → 查看模型」打开工作区。只指定教师时，页面显示应用地址待配置。进入工作区时由该应用独立验证用户。入口只是导航，后台获取网页数据还需要已接通的数据读取方式，可通过价格和逐笔归档成功时间核验。

### 北辰渠道配置和教师网页登录

1. 学校按现有流程更新至 0.14.0 后，在已核实的学校 HTTPS 入口读取 `/.well-known/nebula-beichen-recipient`。该入口只返回收件公钥和算法。学校持久卷内的收件私钥与 HTTPS 身份各自保存。 首次取得证书时，可在浏览器的学校 HTTPS 证书详情中导出单张 DER 格式的服务器证书，用 `Get-FileHash -Algorithm SHA256 <证书路径>` 计算指纹，与校方通过独立渠道确认的 SHA-256 逐字核对。
2. 在管理电脑的仓库目录运行 `scripts/seal-beichen.ps1 -Certificate <校方已核实的证书路径>`。工具先核对学校证书和公钥，再在本机读取四把推理 Key，写出 `internal/infrastructure/schoolseal/school-config.sealed.json`。
3. 渠道密文按学校现有的 GitHub 更新流程交付。服务器更换收件密钥时，可用校方确认的 HTTPS 入口取回新公钥，重新加密渠道配置。
4. 教师在学校可信 HTTPS 入口进入「供应商管理 → 北辰 → 网页登录」，保存北辰网页会话，再点「测试抓取」查看平台、模型和失败项。会话加密持久保存在学校，由后台抓取与续期使用。

### 通过学校正式邮件确认指纹

0.11.1 的收件人已确认收到通知。自 0.12.0 起，实验室 Compose 中的通知批准默认为空。后续如需另发，可经收件人同意设置运行时变量 `LAB_CERTIFICATE_NOTICE_REQUEST_SHA256` 和 `LAB_CERTIFICATE_NOTICE_EXPIRES_AT`。收件地址和申请随机数仅留在请求方。配置中的公开 SHA-256 将一个注册邮箱摘要、一次随机数和发行版本绑定到一封固定内容的通知。未配置批准、超过 24 小时有效窗口、已过期或处于日志邮件模式时不提供发送入口。

在维护 HTTPS 上提交预先批准的请求，格式见 API 文档。收件人从学校正式发件地址取得邮件后，核对版本、学校入口和完整指纹。客户端逐字节比对当前叶证书的 SHA-256，并校验证书有效期和 IP 后，方可使用正常账号或网关凭据。不要从同一公网连接抄出指纹作为独立确认值。一次受理后的 SMTP 错误或超时可能发生在实际投递之后，应先检查邮箱及垃圾箱。

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
