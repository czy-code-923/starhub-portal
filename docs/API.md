# API 与业务状态

前缀 `/portal/v1`。管理员登录后可查询 `/openapi`。购买用户与管理员使用 HttpOnly、SameSite=Lax 会话 Cookie；所有写入需要 `X-CSRF-Token`，从 `/auth/login` 或 `/me` 取得。令牌不保存到 localStorage。注册、重置和支付宝签名回调为各自的独立认证入口。

购买用户：`GET /catalog`、`POST /checkout/quote`、`POST /orders`、`GET /orders`、`GET /orders/{id}`、`POST /orders/{id}/payment`、`POST /orders/{id}/cancel`；`GET /me/service`、`GET /me/subscription/{share|qr|content|nodes|check}?client=mihomo`、`GET /me/devices`、`GET /clients`；`GET/POST /tickets`、`GET/POST /tickets/{id}`。

订单请求示例（价格和额度不能由用户传入）：

```json
{"plan_id":"控制台返回的套餐ID","price_id":"MONTH","coupon":"WELCOME10","request_key":"每次确认下单生成的唯一UUID"}
```

同一用户同一请求键重试返回原订单；改变套餐、周期或优惠码必须换新请求键。下单事务再次核算价格并占用优惠 / 销售名额；报价不预占。

管理员：`GET /admin/{settings|categories|plans|coupons|invitations|customers|orders|jobs|announcements|tickets|audit|control-groups|version}`。分类、套餐、优惠码、邀请、公告支持 POST/PUT；设置仅 PUT；用户支持状态 PUT；失败业务任务支持 POST 重试；版本支持 POST 更新。订单与审计不可手动伪造 / 删除。编辑分类、套餐、优惠码和设置需携带原 `version` 进行乐观并发校验。

金额单位为人民币分；比例优惠的 `amount` 为减免基点，1000 表示减免 10%。`quota_bytes` 存储真实字节；用户界面 GB 使用 1000³，GiB 使用 1024³。

订单状态：`pending → paid → fulfillment queued/active`。取消需先确认支付渠道；超时但收款进入 `late_paid`，需要人工核账，不自动开通。支付事件、开通账本和控制面订单幂等键各自持久化，服务重启后继续。

星枢商业接入：`GET /api/v1/commerce/groups`、`GET /commerce/capacity`；`POST /commerce/validate`；`POST /commerce/entitlements`；`PUT /commerce/accounts/{external_id}/state`。0.4.0 起 `external_id=portal:<32hex order id>`，每单单独开通；旧版 `portal:<customer id>` 仅用于兼容迁移。订单 ID 是永久幂等键，已处理订单重放不重复延长到期。账户停用对该购买用户全部订单身份执行撤销。

本机 `/local/*` API 仅供受限宿主机助手使用独立随机 Worker Token：只允许心跳、领取更新 / 域名任务和报告结果；不接受任意命令。它不使用面板会话，也不向普通管理员浏览器返回令牌。

未来独立前端可沿用上述用户 / 管理员 API；对外第三方长期服务账号、更多支付渠道插件属于下一阶段，不把 Cookie 或管理员密码当作公开 API 密钥。


0.2.0 增补：`GET/PUT /me/profile`、`PUT /me/password`、`POST /auth/verify-email`；注册/重置/改密使用 `password_confirm`。`GET /payment-methods`、`GET/POST /orders/{id}/payment`、`GET /orders/{id}/payment-code`、`GET/POST /orders/{id}/receipt`。

主管理员专属 `GET/PUT /admin/payment-methods`、`GET /admin/payment-methods/{id}/image`、`GET/POST/PUT /admin/administrators`、`POST /admin/demo-plans`；`GET /admin/manual-payments` 和 `GET/PUT /admin/manual-payments/{id}` 需要 payments 权限。审核批准须提交 version、decision=approve、verified_reference、received_amount（分）、received_at（ISO 时间）和 confirmed=true；驳回须填写 note。返回 409 时须刷新数据核对，不应盲目重试。带图像请求采用 image_data 的 PNG/JPEG data URI，服务端限制解码后 1 MiB、尺寸与像素数。

副管理员的功能权限每次请求由数据库实时读取；系统设置、收款方式、版本和团队维护只允许 is_owner=true。具体接口见在线 `/portal/v1/openapi`。

收款方式 `alipay_redpacket` 不需要 `image_data`；提交 `/orders/{id}/receipt` 使用 `redpacket_password`，空值仅在该订单已保存口令时保留原值。普通响应仅含 `redpacket_submitted`，核验权限可通过 `GET /admin/manual-payments/{id}/redpacket` 按需查看并记录审计。0.4.0 起批准口令红包可省略 `verified_reference`，系统生成内部核账编号；仍须实际领取金额、时间和 `confirmed=true`。静态收款码必须提供真实流水。

主管理员 `GET/PUT /admin/telegram`（写入需 version，空 token 保留，clear_token 删除并停用）；`POST /admin/telegram/{check|commands|retry}`。授权对象为 principals 数组，含 user_id、label、scopes、notify；scope 为 overview/orders/payments/redpacket，redpacket 同时需要 payments。配置响应永不返回 Token。`check` 仅验证接入、不发送测试消息；`commands` 仅独立命令模式可用，并拒绝已有 Webhook。`POST /admin/gateway/retry` 重新应用当前域名设置，存在排队任务时返回 409。

0.4.0：新增 `GET /me/services?offset=0`，返回 items、total、offset、limit（25）。`/me/service`、`/me/subscription/{view}`、`/me/devices` 通过 `?service=<order_id>` 选择自己的订单，省略则为最近服务；不能读取其他用户订单。0.6.0 起 content 配置下载关闭并返回 410，改用 share / qr。主管理员 `GET /admin/capacity` 查询实际身份余量，`GET /admin/service-migrations` 查询脱敏迁移结果和服务映射，不返回订阅密钥或迁移密文。

套餐 `display_features=[{"label":"峰值速率","value":"500 Mbps"}]` 最多 12 条，每条名称 24 字、内容 160 字；Vue 以纯文本显示。公告 `pinned` 布尔值、`pin_order` 整数（±100000），支持多条置顶，查询顺序为 pinned 降序、pin_order 升序、created_at 降序。隐藏/取消置顶均用原 POST/PUT 接口。

0.4.0 增加 `approve` 权限和管理员绑定，0.5.0 将其扩展至静态收款码。确认 nonce 绑定机器人 Token、Telegram 数字用户、后台管理员及付款版本，有效 5 分钟、单次使用；权限变化立即生效，实际到账仍由核款人核实。当前权限和使用方法见下文。

0.5.0：`GET /client-help` 返回 `{document,version,platforms}`；`PUT /admin/client-help` 需 `content` 权限，提交 `{document,version}`。文档含 title、intro、apps（id/name/platforms/format/url/steps）、faqs（question/answer）；链接仅 HTTPS，总内容最大 48 KiB。版本不符返回 409。

`DELETE /admin/administrators` 仅主管理员；`DELETE /admin/customers` 需 customers + delete_customers 权限。请求 `{id,version,confirmed:true}`，version 来自列表的 admin_version。删除使账户停用并移出列表，登录与邮箱令牌作废；购买用户的代理撤权排队同步，核账记录保留。`GET /admin/{customers|administrators}?deleted=1` 查询已删除账户；`POST /admin/{customers|administrators}/restore` 使用同样请求体恢复为停用（未验证用户重新验证邮箱），不自动启用代理。主管理员与当前操作人不可删除。

Telegram approve 现在同时支持口令红包与 alipay_static / wechat_static。approve 需 payments 和 administrator_id，只有红包再额外要求 redpacket。静态码核验按钮发出 ForceReply，授权私聊用户回复“真实流水号 金额 YYYY-MM-DD HH:MM”（北京时间），验证金额和付款时间窗口后生成二次确认按钮；首次有效填写后不可覆盖。仅 notify_only 模式不消费按钮或回复；审批必须选择 polling 专用机器人。截图、口令或填写流水都不会直接开通，最终确认仍使用后台相同的事务审核、流水去重和幂等开通任务。


0.6.0：`GET /me/services`、`GET /orders`、`GET /admin/orders` 附带 `service_state`，可能为 unknown / active / disabled / expired / exhausted / deleted / invalid；`remote_checked_at`（订单列表为 service_checked_at）表示最近成功核验时间。`GET /me/service?service=<order>` 的 service.state 为实时身份状态，删除也返回 200 的状态对象。失效时 share / qr / nodes 返回 410，check 返回 ready=false；content 始终返回 410。404 仍用于不存在或不属于当前用户的订单。断网、鉴权失败、响应格式不完整返回 503 或保留上次成功状态，不标记删除，不重新开通。

`GET /admin/capacity` 分别返回核心真实 identities / identity_limit（仍用于容量准入）、paid_orders（核验通过的历史订单）、portal_services（当前有效订阅）、invalid_services、unchecked_services、registered_customers 和 services_checked_at。非已付款订单不计入服务统计。后台快照间隔 60 秒，数据库仅缓存明确的计量字段，不缓存代理凭据。

`GET /admin/record-retention?kind=jobs|audit` 需对应 jobs/audit 权限，返回固定保留策略、条数和 can_clear。`POST` 同路径仅主管理员可执行，需 CSRF 和 `{confirmed:true}`；audit 清空后留下此次清理标记，jobs 仅删除 succeeded / rolled_back。工作中和待重试任务、财务与幂等凭据不可通过此接口删除。


0.7.0：`GET /catalog` 增加每套餐 `stock_limit`、`remaining_stock`、`sold_out` 及响应 `stock_checked_at`；不限名额时 remaining_stock=null。0.9.0 起按有效独立身份去重计算；有效期内续费不重复占用，过期或删除释放；新购和过期后续费 pending/已付待开通预占空位；取消未付款订单释放，隐藏订阅记录不释放。可见套餐页每 10 秒刷新，创建订单在数据库事务锁中复核余量。

`GET /me/renewal?service=<原订阅订单ID>` 返回可用续费价格、服务状态、注意事项和已有待处理续费订单，仅本用户可读。`POST /checkout/quote` 与 `POST /orders` 可加 `renewal_of=<原服务订单ID>`：须同套餐且允许续费，优惠码规则沿用结算服务；同一订阅只允许一个待支付 / 待生效续费。订单列表和详情增加 renewal_of 和 service_id，客户端应使用 service_id 进入原订阅。幂等键绑定续费目标，不可改为其他订阅或新购。已核验续费通过星枢 0.25.1+ commerce/renewals 延长期限；不会新增 service_accounts 或重新创建已删除身份，清理任务历史后也不会重复续期。续费只延长时长，不改当前额度、用量、权限和重置时间。

0.8.0：正式套餐保存、结算和新身份开通须实时核验所选用户组存在且提供所有已勾选的可计费协议。`GET /catalog` 的 protocols 仅返回实际选择与所选组的交集，protocols_verified=false 时客户端显示待同步，不声明支持任何协议。分组元数据缓存最多 30 秒，写入和结算不使用缓存。

`DELETE /admin/plans`（plans 权限）请求 `{id,version,confirmed:true}` 软删除并下架套餐，阻止新购和新续费订单；已有有效订单按快照处理，原服务及财务账本保留。`GET /admin/plans?deleted=1` 查看已删除套餐；`POST /admin/plans/restore` 同样请求体恢复为下架状态。编辑已删除套餐返回 409。

`DELETE /me/services/{id}` 请求 `{confirmed:true}`，只移除本人失效/删除服务的列表记录；有效、状态未知及仍有待处理续费的服务返回 409。`GET /me/services?hidden=1` 分页查询已移除记录；`POST /me/services/{id}/restore` 恢复显示，不恢复代理权限。隐藏后不展示导入链接，订单返回 subscription_hidden=true。

续费截止为星枢原服务 expires_at + 72 小时（严格小于截止时间）。`GET /me/service` 返回 renewal_window_open、renewal_deadline、renewal_unavailable_reason；`GET /me/renewal` 返回截止、remaining_stock 和不可用 reason。报价/下单重新核验原服务、套餐销售余量；同一原订阅的订单按身份去重计数；有效期内续费沿用原名额，过期后重新开通须有空位且待付款预留。订单 expires_at 不超过续费截止；报价返回 renewal_deadline。开始付款、提交凭证、审批及开通均验证截止。期限内实际到账可在之后核验，核验时间不替代实际付款时间；迟到支付宝回调进入 late_paid 人工核账，不自动续期。已核验历史权益不追溯撤销。

0.9.0：`stock` 表示该套餐当前有效独立订阅人数上限（不是历史成交笔数）。有效期内、启用且未删除的身份占名额，用尽流量但未到期仍占用；未同步状态保守保留，查询失败不推断删除。当前有效订阅的续费不重复占位；过期或已删除释放，过期后仍按 72 小时窗口续费并重新预占空位。已删除的身份不能重建原凭据，应新购独立服务。待付款、已付待开通和待退款订单保留必要预占，以原服务 ID 去重。名额校验前强制读取完整远端身份快照，事务中再次去重计数，避免新购与重新开通超卖。

`GET /catalog` 增加 effective_members 和 stock_synced；元数据/人数页面每 10 秒同步，expires_at 依据服务端当前时间即时判断。`GET /me/renewal` 的 requires_seat 表示原身份是否需要重新占位，满额不阻止已有有效身份续费。

管理员修改套餐 group_ids/protocols 时递增独立 policy_revision，并事务性排队 policy 任务。通过星枢 0.25.2+ `PUT /commerce/accounts/{external_id}/policy` 同步各已购身份，body 绑定 user_id、subscription_id、source_id=plan_id 和递增 revision。接口不修改任何会计字段、凭据或有效期。`GET /admin/plans` 增加 effective_members、policy_pending、policy_failed；后台任务支持重试。`GET /me/service` 增加 policy_synced/policy_warning，订阅 check 在权限同步未完成时返回 ready=false 和明确 policy_pending，share 则 503，不伪装成客户端协议不兼容。客户端更新原订阅后取得新授权节点。
