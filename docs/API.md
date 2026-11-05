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

0.4.0：新增 `GET /me/services?offset=0`，返回 items、total、offset、limit（25）。`/me/service`、`/me/subscription/{view}`、`/me/devices` 通过 `?service=<order_id>` 选择自己的订单，省略则为最近服务；不能读取其他用户订单。下载 content 的 auto 格式默认 Mihomo，并返回附件文件名。主管理员 `GET /admin/capacity` 查询实际身份余量，`GET /admin/service-migrations` 查询脱敏迁移结果和服务映射，不返回订阅密钥或迁移密文。

套餐 `display_features=[{"label":"峰值速率","value":"500 Mbps"}]` 最多 12 条，每条名称 24 字、内容 160 字；Vue 以纯文本显示。公告 `pinned` 布尔值、`pin_order` 整数（±100000），支持多条置顶，查询顺序为 pinned 降序、pin_order 升序、created_at 降序。隐藏/取消置顶均用原 POST/PUT 接口。

Telegram 新增 `approve` 权限，同时需 payments/redpacket 及 `administrator_id`，保存时和每次确认时检查所绑定管理员仍有核款权限。只支持口令红包二次确认批准，不执行实际领取，不自动批准静态码。确认 nonce 绑定机器人 Token、Telegram 数字用户、后台管理员、付款版本，有效 5 分钟、单次使用；权限变化立即生效。
