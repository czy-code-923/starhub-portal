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

星枢商业接入：`GET /api/v1/commerce/groups`；`POST /commerce/validate`；`POST /commerce/entitlements`；`PUT /commerce/accounts/{portal_customer_id}/state`。`external_id` 是 `portal:<32hex customer id>`，`order_id` 是永久的购买订单 ID。开通接口为一个原子操作：建立/续费独立用户，更新原组成员资格及协议白名单、额度、到期、设备上限，创建/复用独立订阅并同步到期时间。已处理订单重放不会重复延长到期时间。

本机 `/local/*` API 仅供受限宿主机助手使用独立随机 Worker Token：只允许心跳、领取更新 / 域名任务和报告结果；不接受任意命令。它不使用面板会话，也不向普通管理员浏览器返回令牌。

未来独立前端可沿用上述用户 / 管理员 API；对外第三方长期服务账号、更多支付渠道插件属于下一阶段，不把 Cookie 或管理员密码当作公开 API 密钥。


0.2.0 增补：`GET/PUT /me/profile`、`PUT /me/password`、`POST /auth/verify-email`；注册/重置/改密使用 `password_confirm`。`GET /payment-methods`、`GET/POST /orders/{id}/payment`、`GET /orders/{id}/payment-code`、`GET/POST /orders/{id}/receipt`。

主管理员专属 `GET/PUT /admin/payment-methods`、`GET /admin/payment-methods/{id}/image`、`GET/POST/PUT /admin/administrators`、`POST /admin/demo-plans`；`GET /admin/manual-payments` 和 `GET/PUT /admin/manual-payments/{id}` 需要 payments 权限。审核批准须提交 version、decision=approve、verified_reference、received_amount（分）、received_at（ISO 时间）和 confirmed=true；驳回须填写 note。返回 409 时须刷新数据核对，不应盲目重试。带图像请求采用 image_data 的 PNG/JPEG data URI，服务端限制解码后 1 MiB、尺寸与像素数。

副管理员的功能权限每次请求由数据库实时读取；系统设置、收款方式、版本和团队维护只允许 is_owner=true。具体接口见在线 `/portal/v1/openapi`。

0.3.0：收款方式增加 `alipay_redpacket`，不需要 `image_data`；提交 `/orders/{id}/receipt` 使用 `redpacket_password`，空值仅在该订单已保存口令时保留原值。普通响应仅含 `redpacket_submitted`，核验权限可通过 `GET /admin/manual-payments/{id}/redpacket` 按需查看并记录审计。口令不等于到账凭证，批准仍需真实流水、金额、领取时间和 confirmed=true。

主管理员 `GET/PUT /admin/telegram`（写入需 version，空 token 保留，clear_token 删除并停用）；`POST /admin/telegram/{check|commands|retry}`。授权对象为 principals 数组，含 user_id、label、scopes、notify；scope 为 overview/orders/payments/redpacket，redpacket 同时需要 payments。配置响应永不返回 Token。`check` 仅验证接入、不发送测试消息；`commands` 仅独立命令模式可用，并拒绝已有 Webhook。`POST /admin/gateway/retry` 重新应用当前域名设置，存在排队任务时返回 409。
