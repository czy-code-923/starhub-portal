# 星枢用户中心 · Starhub Portal

面向购买用户的独立套餐、支付与订阅面板。管理员管理分类、推荐、套餐、优惠码、邀请和工单；购买用户只访问自己的订单、流量、设备和订阅。Vue 3 + TypeScript、Go REST API、PostgreSQL，使用数据库任务队列，无 Redis 依赖。

本项目配合 [星枢控制面](https://github.com/czy-code-923/czy-code) **0.25.0+ / `commerce_v1`**。主机、内核、节点和用户组仍由控制面管理；购买面板选择已有组与允许协议，不另外部署代理内核。

0.4.0 起每个购买订单单独创建代理身份、配额和订阅，同套餐多次购买分别管理。旧版尚未到期的合并权益自动迁移：旧链接作废，原剩余字节按 GB → 同数字 GiB 扩容后按订单额度比例分配，保留原合并权益的到期和重置日期；请购买用户重新复制各订单链接。历史订单、付款和核账记录保留。已有套餐转为同数字 GiB，20 GiB 与 Clash 的 20 G 对齐。

套餐可逐条编辑最多 12 条展示信息（速率、适用场景等），仅作为介绍，不会修改实际限速。公告支持多条置顶，数字越小越靠前，同序号按新到旧显示。用户端不显示后台计费倍率。

升级购买面板时，同机且已有受限本机星枢更新器会自动请求本版所需的官方兼容更新；外部星枢须先由管理员升级到 0.25.0+。更新包均校验 SHA-256，失败保留恢复路径。版本页显示兼容状态、独立身份容量和本面板容器资源。

## Linux 一键部署

Debian / Ubuntu、RHEL / Rocky / AlmaLinux / Fedora 等主流 Linux，x86_64 / aarch64。安装器检查 Python 3.10+、可用的 Docker Engine 和 Compose v2；缺少 Docker 时从 Docker 官方源安装。不会重装宝塔、Nginx、Docker 或原有控制面。

```sh
curl --proto '=https' --tlsv1.2 -fsSL https://raw.githubusercontent.com/czy-code-923/starhub-portal/main/scripts/install.sh -o /tmp/install-starhub-portal.sh && sudo sh /tmp/install-starhub-portal.sh
```

自动检测本机 IPv4 / IPv6 并输出入口，不绑定域名也能使用。NAT 主机请显式填写外部可达 IP。以下命令让你输入本次部署 VPS 的地址和端口，不含固定生产 IP：

```sh
printf '本台 VPS 的公网 IPv4 或 IPv6: '; read -r PORTAL_IP
printf '面板端口（回车用 18088）: '; read -r PORTAL_PORT
sudo sh /tmp/install-starhub-portal.sh --ip "$PORTAL_IP" --port "${PORTAL_PORT:-18088}" --admin-email you@example.com --ask-password
```

默认目录 `/opt/starhub-portal`，独立 TCP 端口 `18088`；数据库不映射公网端口。`--ip` 只填 IP，不加协议、路径或端口；IPv6 自动生成带方括号的入口。`--port` 为 1024–65535，监听所有 IPv4（IPv6 地址则监听 IPv6）；可用 `--bind 127.0.0.1` 仅本机访问。主机或服务商防火墙须放行实际入口端口，脚本不会关闭防火墙。

可选 `--origin https://你的购买面板域名` 设置首选入口，IP 入口单独保留。也可先不绑定域名，稍后在管理员「站点与连接」设置域名与 HTTPS。与星枢同机时自动关联该域名已就绪的证书及本站反代，不复制私钥、不改其他站点；单独部署且 80/443 空闲时可使用本站 Caddy。已有其他宝塔 / Nginx 时生成本站反代片段，由管理员加入对应站点。**申请证书与建立网站反代是两步**；保存或点击“重试域名配置”后须等待端到端验证成功。

每次新安装生成独立的数据库密码、AES 主密钥、更新助手令牌和随机管理员密码；无通用登录令牌。重复运行安装保留已有安装与凭据，不覆盖业务数据库。

## Windows 一键部署

在管理员 PowerShell 执行。缺少 Python / Docker Desktop 时通过 winget 安装；Docker Desktop 首次启动、WSL 初始化或重启必须由本机用户完成，脚本会明确提示后停止，完成后重跑。要求 **Linux 容器模式**，不支持 Windows 容器。

```powershell
$f=Join-Path $env:TEMP 'install-starhub-portal.ps1'; Invoke-WebRequest https://raw.githubusercontent.com/czy-code-923/starhub-portal/main/scripts/install.ps1 -OutFile $f; & $f
```

默认目录 `C:\ProgramData\StarhubPortal`。同样运行 Linux 容器；更新助手使用隐藏的计划任务，登录后运行。

指定本次 Windows 主机地址和端口：

```powershell
$PortalIP=Read-Host '本机可达的 IPv4 或 IPv6'; & $f -PublicIP $PortalIP -Port 18088 -AdminEmail you@example.com -AskPassword
```

## 查询面板地址

```sh
sudo python3 /opt/starhub-portal/scripts/manage.py status
```

分别显示 IP 入口、首选域名入口、实际运行版本和健康状态；无需域名或控制面令牌。自定义目录请加 `--root /你的安装目录`，Windows 使用 `python "$env:ProgramData\StarhubPortal\scripts\manage.py" status`。域名故障可使用列出的 IP＋端口；公网 HTTP 适合临时恢复入口，日常登录使用 HTTPS。

## 查看本购买面板的初始管理员凭据

不是宝塔令牌，也不是星枢控制面令牌。仅在本机管理员终端执行，勿公开输出：

```sh
sudo python3 /opt/starhub-portal/scripts/manage.py credentials
```

恢复管理员密码并注销该管理员所有会话：

```sh
sudo python3 /opt/starhub-portal/scripts/manage.py admin-reset --admin-email you@example.com
```

## Linux 一键卸载（保留数据）

```sh
sudo python3 /opt/starhub-portal/scripts/manage.py uninstall
```

只移除本项目容器、网络和自己的更新助手。保留订单、用户、配置、主密钥和备份；不卸载 Docker，不清理其他容器，不删除星枢或宝塔。

彻底删除本购买面板数据，**无法撤销**，与普通卸载分开执行：

```sh
sudo python3 /opt/starhub-portal/scripts/manage.py uninstall --purge-data --confirm DELETE-PORTAL-DATA
```

## Windows 卸载（保留数据）

```powershell
python "$env:ProgramData\StarhubPortal\scripts\manage.py" uninstall
```

Windows 彻底删除数据：

```powershell
python "$env:ProgramData\StarhubPortal\scripts\manage.py" uninstall --purge-data --confirm DELETE-PORTAL-DATA
```

## 首次配置

1. 登录管理员，在「站点与连接」填写星枢 HTTPS 地址和本项目专用的受限 API 密钥。权限为 `commerce:read`、`commerce:write`、`users:read`、`devices:read`、`subscriptions:read`、`subscriptions:links`、`profiles:read`。
2. 在星枢建立专用于售卖的可计费用户组，加入启用独立用户身份、支持个人计量且运行就绪的节点。组内不能包含共享密钥节点；例如建 AnyTLS / Hysteria 2 组，再在套餐中只允许 `anytls` / `hysteria2`。Snell 或 SS 是否可售取决于该节点是否使用独立身份与支持个人计量；普通共享密钥节点不可售。
3. 配置 TLS SMTP（465 / 587）。注册默认需要邀请和邮箱验证，可叠加白名单、人工审核与总名额；未配置邮件服务时不允许注册。无后台造假“已验证”快捷入口。
4. 创建分类，添加套餐、价格周期、流量额度、设备限制和时间窗口。首次购买按当前时间开通；未到期续费从原到期时间延长，同一用户保留稳定订阅。MVP 同一账户只允许续费原套餐，套餐切换需人工安排。
5. 在「收款方式」启用支付宝 / 微信静态码，或支付宝口令红包，均需人工核款；支付宝官方在线支付独立可选，须配置获准的商户应用和密钥。
6. 创建邀请码；有权限的管理员可反复查看和复制，支持邮箱、有效时间和使用次数。旧版仅存哈希的邀请需显式重新生成。

## MVP 的业务边界

- 订单金额以服务端计算为准，整数分存储。优惠码支持减免金额、减免比例、时间、套餐 / 周期 / 用户范围、限次和新客；待支付订单占用名额，支付渠道关闭或确认未创建交易后释放。
- 一个订单最多开通一次。回调校验 RSA2 签名、应用、商户、交易号、优惠后实付金额和付款时间；重复回调幂等。不能把付款跳转页面当作支付成功依据。无签名或金额不符不会发放权益。
- 超时到账进入 `late_paid`，不自动发放服务，需管理员核对支付宝交易并人工退款。**MVP 不发起自动退款、不承诺已完成真实商户支付验证。** 不支持零元优惠后订单，最低实付 1 分。
- 只有节点配置和独立身份已同步、计量就绪，订单才显示「已生效」。Agent / 上游暂时失败保留已付订单并重试；任务最终失败可由管理员重试，不丢失订单。
- 商业用户的协议白名单同时作用于代理身份下发及所有订阅格式，包括直接 `/s` 分享链接；后续组中加入共享节点不会泄漏到商业订阅。原控制面的自用用户不受影响。
- 用户额度、余额、到期与设备数读取控制面，默认 GB，可选 GiB / 自动。设备限制使用控制面客户端回传机制；缺少稳定设备标识时只能限制订阅访问记录，不能声称可识别或限制所有物理设备。
- 真实额度耗尽、过期和停用依赖控制面向 Agent 同步撤销独立身份，不只是阻止下载订阅。严格实时性取决于 Agent 的同步周期和连接状况。
- 本版注册总上限 500，入站身份容量沿用控制面的 500 上限。适合先小规模验证；不是无限规模计费平台。

## 更新与恢复

管理员「版本与更新」检查 GitHub 正式发布，可一键更新。宿主机助手只接受指定仓库的正式版本、校验 GitHub 资产 SHA-256，不执行任意 shell。先停止本站应用、备份 PostgreSQL、保留旧程序与环境文件，再构建并健康检查；失败尝试恢复原数据库和程序，结果写入任务。主密钥不可丢失，备份也含敏感数据。

命令行更新：

```sh
sudo python3 /opt/starhub-portal/scripts/manage.py update
```

指定正式版本：

```sh
sudo python3 /opt/starhub-portal/scripts/manage.py update --version 0.4.0
```

助手未运行时：

```sh
sudo python3 /opt/starhub-portal/scripts/manage.py repair-helper
```

## 手动构建与本地开发

见 [开发与手动构建](docs/DEVELOPMENT.md)、[API 和业务状态](docs/API.md)、[安全和运维](docs/OPERATIONS.md)。源码含数据库迁移和隔离测试；生产不自动写入演示用户或虚假订单；主管理员可点击“生成模拟套餐”，按需创建 3 类 6 个可编辑的展示套餐。模拟套餐不可购买。

暂不包含推广返利、余额钱包、自动退款、跨套餐折算。Telegram 命令模式须使用独立机器人；仅通知模式可复用已有机器人，不消费其消息。

## 个人收款码、账户与管理权限（0.2.2）

- 主管理员在「收款方式」上传支付宝或微信个人静态收款码，可分别开关、编辑名称与说明。支持 PNG/JPEG，保存时最多 1 MiB。无需支付宝官方应用；官方在线支付仍可独立选配。
- 用户选择收款方式后展示订单金额和当时的收款码快照；管理员更换二维码不会改变已创建订单的收款信息。用户提交流水或截图只进入「待核款」，不会自动开通。
- 「付款核验」必须按收款账户的实际到账记录确认流水、优惠后准确金额和付款窗口内的到账时间；同一真实流水不可重复核销。已提交凭证的订单保留至原到期后 48 小时供审核，逾期关闭。不能核验自己的订单。不提供个人静态码的自动到账回调或自动退款。
- 注册模式可选关闭、邀请码、邮箱白名单、直接邮箱验证；邮箱 SMTP 未配置时不开放注册。注册和密码重置均需输入两次相同密码。用户可编辑昵称、验证后更换邮箱、使用当前密码修改密码；邮箱/密码变更会注销所有登录会话。
- 「管理员」仅主管理员可用，可创建或停用按功能授权的副管理员。副管理员无权修改主管理员、系统密钥、收款码或版本更新；付款审核等权限需单独勾选。
- 邀请码加密保存，授权管理员可反复查看；优惠码可反复查看和复制。API 密钥仍使用不可还原的哈希，创建时需妥善保存，这与邀请码不同。
- 模拟套餐：轻量体验（20/50 GB）、日常畅连（100/200 GB）、大流量精选（300/500 GB），含月/季/年价格示例。只有编辑绑定已就绪的可计费用户组、确认协议及价格并关闭模拟标记后才可售卖；不可把模拟套餐视为真实可用权益。

## 口令红包与 Telegram（0.3.0）

- 「收款方式」可启用支付宝口令红包，无需上传二维码。用户发送金额与订单一致的单个红包，将口令提交到该订单；口令加密保存，同一口令不能绑定另一订单，列表和普通通知不返回完整口令。不能把支付密码、登录密码或验证码当作红包口令。
- 管理员在「人工核款」按需查看口令（记录审计），在支付宝官方客户端自行领取并核实真实到账，再填真实流水、准确金额和领取时间批准。提交口令、截图或 Telegram 通知均不会自动开通；红包是否有效、是否已被领取由支付宝确认。本项目不使用个人红包的非官方自动领取接口。
- 主管理员在独立的「Telegram 通知」页面填写 Bot Token，按 Telegram **数字用户 ID** 授予概览、订单、核款及额外的口令查看权限。接收者须先私聊启动机器人；仅发送到已授权的私聊。
- 命令 `/help`、`/overview`、`/orders`、`/payments`、`/redpacket 完整订单ID` 逐次校验权限。完整口令仅在同时具有核款和口令查看权限的私聊查询中返回，不提供机器人自动审批或领取红包。新订单、付款提交、审核结果及开通状态使用持久化队列通知，失败退避重试并可手动重试。
- 独立命令模式拒绝接管已有 Webhook；不会删除其他程序的 Webhook。已有机器人被原后台轮询时请选择“仅发送通知”，或创建专用于购买面板的机器人。参考 [Telegram Bot API](https://core.telegram.org/bots/api) 和 [支付宝红包服务协议](https://render.alipay.com/p/yuyan/180020010001201259/gift/agreement.htm)。
