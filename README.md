# 星枢用户中心 · Starhub Portal

面向购买用户的独立套餐、支付与订阅面板。管理员管理分类、推荐、套餐、优惠码、邀请和工单；购买用户只访问自己的订单、流量、设备和订阅。Vue 3 + TypeScript、Go REST API、PostgreSQL，使用数据库任务队列，无 Redis 依赖。

本项目配合 [星枢控制面](https://github.com/czy-code-923/czy-code) **0.25.4+ / `commerce_v1`**。主机、内核、节点和用户组仍由控制面管理；购买面板选择已有组与允许协议，不另外部署代理内核。

0.4.1 起每个新购订单单独创建代理身份、配额和订阅，同套餐多次购买分别管理。旧版尚未到期的合并权益自动迁移：旧链接作废，原剩余字节按 GB → 同数字 GiB 扩容后按订单额度比例分配，保留原合并权益的到期和重置日期；请购买用户重新复制各订单链接。历史订单、付款和核账记录保留。已有套餐转为同数字 GiB，20 GiB 与 Clash 的 20 G 对齐。

套餐可逐条编辑最多 12 条展示信息（速率、适用场景等），仅作为介绍，不会修改实际限速。公告支持多条置顶，数字越小越靠前，同序号按新到旧显示。用户端不显示后台计费倍率。

升级购买面板时，同机且已有受限本机星枢更新器会自动请求本版所需的官方兼容更新；外部星枢须先由管理员升级到 0.25.4+。更新包均校验 SHA-256，失败保留恢复路径。版本页显示兼容状态、独立身份容量和本面板容器资源。

## 运营功能

客户端教程可在“客户端教程 → 编辑教程”维护，支持按平台配置步骤、说明链接和常见问题；副管理员需获得“客户端教程编辑”权限。套餐类别统一使用相同卡片结构。

Telegram 核款需要专用机器人的独立轮询模式，并明确授权现有私聊用户“付款核验”和“核验红包 / 收款码并开通”，绑定有核款权限的后台管理员；红包还需口令查看权限。红包必须先在支付宝实际领取再二次确认；支付宝/微信静态码通过回复真实流水、金额和北京时间后再确认，不会根据截图或口令自动认定付款成功。

副管理员和购买用户支持删除与恢复。删除撤销登录，用户的全部代理撤权通过队列同步；订单核账记录保留。恢复默认停用，未验证账户须重新验证。删除主管理员或自己会被拒绝。副管理员删除用户需额外分配删除权限。

0.6.0：用户在“订阅与设备”先选择订单，再逐个查看、复制各客户端格式的订阅链接或二维码；不提供配置文件下载，旧 content API 返回 410。格式列表沿用星枢兼容目录，不为每种客户端重复开通身份。链接按需读取，切换订单会清除上一订单的链接与二维码。

星枢删除、停用、到期或额度耗尽的身份会同步为对应失效状态，用户页与管理员订单列表可见，不会自动重建已删除身份。已删除订单身份不计入当前有效订阅。容量页分别显示星枢现存身份、历史已核验订单、有效服务和失效服务，未付款订单不计入已核验数量。后台每分钟批量验证，打开具体服务或取链接时实时复核；连接失败或不完整响应不会被当成删除。

操作记录自动保留最近 30 天 / 最多 10,000 条，已完成或已回滚任务保留最近 30 天 / 最多 2,000 条，每 5 分钟检查。主管理员可在相应页面手动清理；排队、处理中与失败待重试任务保留。支付订单、核款流水及幂等开通凭据独立保留，清理日志不会重新发放权益。Docker 日志继续使用大小与文件数轮转。PostgreSQL 会复用清理出的表空间，常规清理不强制执行阻塞业务的 VACUUM FULL。

0.9.1：订阅导入保留简洁布局，通过客户端下拉框切换链接与二维码，不提供配置下载。套餐卡片显示限售套餐当前剩余名额，可见页面每 10 秒刷新；按当前有效独立订阅人数占用名额；有效期内续费沿用原名额，过期或删除释放，新购及过期后续费预占空位，取消后释放，下单在数据库锁内再次核验，避免并发超卖。0 表示不限名额。

更改套餐用户组与协议会同步到该套餐已购订阅，通过受限商业 API 修改权限，不改原链接、UUID、到期、额度或已用量。后台任务支持失败重试，前台导入会先确认最新权限同步；客户端需刷新原订阅以取得新节点。

套餐协议只展示实际勾选且由所选星枢用户组提供的可计费协议，更换用户组会清理不兼容选择。示例套餐不预填协议。管理员可删除套餐并从“显示已删除套餐”恢复为下架状态；已开通订阅、历史订单与付款不受影响。用户可移除失效订阅记录，也可恢复显示；不会销毁财务记录；有效人数由远端身份状态决定，隐藏记录不影响名额。

“订阅与设备 → 选择原订阅 → 续费此订阅”可选择周期并使用优惠码。续费仍须按原付款方式提交凭证和核款，支持原有 Telegram 审批；确认后在原到期时间上按自然月顺延，已到期则从当前时间起算。保留原身份、UUID、订阅链接、额度、已用流量及月重置日，不新增服务或独立身份，有效期内续费不重复占名额，已过期的订阅续费须有剩余空位。未到期或到期后不足 72 小时内可下单并付款，超过期限禁止续费；在期限内已付款的订单可等待后台核款。套餐须启用“允许续费”，已删除、停用或撤销的订阅不能通过续费重新创建。续费金额使用当前启用周期的价格；续费不改变原订阅的流量和设备权限设置；用户组与协议始终跟随当前套餐设置。

## Linux 一键部署

Debian / Ubuntu、RHEL / Rocky / AlmaLinux / Fedora 等主流 Linux，x86_64 / aarch64。安装器检查 Python 3.10+、可用的 Docker Engine 和 Compose v2；缺少 Docker 时从 Docker 官方源安装。不会重装宝塔、Nginx、Docker 或原有控制面。Linux 后台常驻与开机自启使用 systemd。

```sh
(set -eu; f=$(mktemp); trap 'rm -f "$f"' EXIT HUP INT TERM; curl --proto '=https' --tlsv1.2 -fsSL https://raw.githubusercontent.com/czy-code-923/starhub-portal/main/scripts/install.sh -o "$f"; sh "$f")
```

root 登录可直接执行，无需安装 `sudo`；普通用户有 `sudo` 时安装器自动提权，否则明确提示先用 `su -` 或 SSH 登录 root。以下 Linux 管理命令均在 root 终端执行；普通用户可先 `sudo -i`（须已安装并授权）或 `su -`。

自动检测本机 IPv4 / IPv6 并输出入口，不绑定域名也能使用。NAT 主机请显式填写外部可达 IP。以下命令让你输入本次部署 VPS 的地址和端口，不含固定生产 IP：

```sh
printf '本台 VPS 的公网 IPv4 或 IPv6: '; read -r PORTAL_IP
printf '面板端口（回车用 18088）: '; read -r PORTAL_PORT
(set -eu; f=$(mktemp); trap 'rm -f "$f"' EXIT HUP INT TERM; curl --proto '=https' --tlsv1.2 -fsSL https://raw.githubusercontent.com/czy-code-923/starhub-portal/main/scripts/install.sh -o "$f"; sh "$f" --ip "$PORTAL_IP" --port "${PORTAL_PORT:-18088}" --admin-email you@example.com --ask-password)
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

## 后台常驻、开机自启与日志

0.10.2 起，以下命令启动已有安装的应用、数据库、本站独立网关（如已创建）及更新助手，并启用 Linux 开机自启。服务运行在后台，关闭 SSH 不会退出，无需 `nohup`：

```sh
python3 /opt/starhub-portal/scripts/manage.py start
```

新安装自动启用 Docker 和更新助手；应用、数据库和本站独立网关使用 `restart: unless-stopped`。`start` 也可恢复保留数据卸载后的服务，不重新生成管理员或密钥。旧版先用下文 `update` 升到 0.10.2，再使用新增命令。不停止或重启其他项目使用的 Docker daemon。

暂停本面板并关闭其自动启动（保留所有数据），之后用 `start` 恢复：

```sh
python3 /opt/starhub-portal/scripts/manage.py stop
```

重启本面板、查询入口状态、读取最近 100 条应用与数据库日志：

```sh
python3 /opt/starhub-portal/scripts/manage.py restart
python3 /opt/starhub-portal/scripts/manage.py status
python3 /opt/starhub-portal/scripts/manage.py logs
```

Linux 查看更新助手状态与最近日志：

```sh
systemctl status starhub-portal-helper.service --no-pager
journalctl -u starhub-portal-helper.service -n 100 --no-pager
```

不要在版本更新或数据库恢复期间执行 `stop` / `restart`。没有运行 systemd 的容器环境会明确拒绝自动启动操作，请在宿主机部署或自行配置对应的服务管理器。Windows 在管理员 PowerShell 用 `python "$env:ProgramData\StarhubPortal\scripts\manage.py" start`；须已启动 Docker Desktop Linux 容器，助手在管理员登录后运行。Docker Desktop 的“登录时启动”需在其设置中开启，不承诺未登录时自动运行。

## 查询面板地址

```sh
python3 /opt/starhub-portal/scripts/manage.py status
```

分别显示 IP 入口、首选域名入口、实际运行版本和健康状态；无需域名或控制面令牌。自定义目录请加 `--root /你的安装目录`，Windows 使用 `python "$env:ProgramData\StarhubPortal\scripts\manage.py" status`。域名故障可使用列出的 IP＋端口；公网 HTTP 适合临时恢复入口，日常登录使用 HTTPS。

## 查看本购买面板的初始管理员凭据

不是宝塔令牌，也不是星枢控制面令牌。仅在本机管理员终端执行，勿公开输出：

```sh
python3 /opt/starhub-portal/scripts/manage.py credentials
```

恢复管理员密码并注销该管理员所有会话：

```sh
python3 /opt/starhub-portal/scripts/manage.py admin-reset --admin-email you@example.com
```

## Linux 一键卸载（保留数据）

```sh
python3 /opt/starhub-portal/scripts/manage.py uninstall
```

只移除本项目容器、网络和自己的更新助手。保留订单、用户、配置、主密钥和备份；不卸载 Docker，不清理其他容器，不删除星枢或宝塔。

彻底删除本购买面板数据，**无法撤销**，与普通卸载分开执行：

```sh
python3 /opt/starhub-portal/scripts/manage.py uninstall --purge-data --confirm DELETE-PORTAL-DATA
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
python3 /opt/starhub-portal/scripts/manage.py update
```

指定正式版本：

```sh
python3 /opt/starhub-portal/scripts/manage.py update --version 0.10.2
```

助手未运行时：

```sh
python3 /opt/starhub-portal/scripts/manage.py repair-helper
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

## 入口与域名一键操作

以下是购买面板命令，读取当前安装的 IP 和端口，不固定任何 VPS。自定义安装目录时，将 `/opt/starhub-portal` 改成你的 `--root` 目录。

查询当前购买面板地址及版本：

```bash
python3 /opt/starhub-portal/scripts/manage.py status
```

开启购买面板域名（请把示例域名改成你的域名；需 DNS 已指向本机，并准备证书或让独立 Caddy 自动签发）：

```bash
python3 /opt/starhub-portal/scripts/manage.py domain-enable --domain buy.example.com
```

关闭购买面板域名，切换为安装时记录的 IP＋端口（HTTP 为明文，因此需明确添加参数）：

```bash
python3 /opt/starhub-portal/scripts/manage.py domain-disable --allow-http
```

命令保存设置并排队入口任务，可在后台“版本与更新”查看结果。仅修改本项目拥有的网关，不接管其他宝塔网站；回环部署不会自动扩大到公网监听，须通过原 SSH 隧道管理。已启用官方支付宝 HTTPS 回调时，先在后台停用该渠道再切换 HTTP；个人收款码不受此限制。Windows 用 `python "$env:ProgramData/StarhubPortal/scripts/manage.py"` 调用相同行为，或按自定义 `--root` 路径；Docker Desktop 需使用 Linux 容器模式。

上述操作只管理购买面板入口。星枢总管理端查询/临时 IP 救援命令见[星枢安装说明](https://github.com/czy-code-923/czy-code#readme)，不要关闭 Agent 仍依赖的控制域名。购买面板切换域名不会修改星枢、Agent 或独立订阅域名。

## 自动订阅与明确客户端

自动链接不固定格式，由实际客户端请求头识别；管理端检查会验证至少一种完整配置格式，而不把服务端 HTTP 请求误认作用户客户端。AnyTLS 等协议可选择 Mihomo、sing-box 或 Hiddify；不支持协议的明确客户端仍会提示更换格式。sing-box 扫码请选择专属客户端格式。页面提供订阅链接与二维码，不提供配置下载。
