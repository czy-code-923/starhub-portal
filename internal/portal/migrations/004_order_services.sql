ALTER TABLE service_accounts ADD COLUMN order_id text REFERENCES orders(id);
ALTER TABLE service_accounts ADD COLUMN external_id text;
ALTER TABLE service_accounts ADD COLUMN legacy_combined boolean NOT NULL DEFAULT true;
UPDATE service_accounts SET order_id=last_order_id,external_id='portal:'||customer_id;
ALTER TABLE service_accounts ALTER COLUMN order_id SET NOT NULL;
ALTER TABLE service_accounts ALTER COLUMN external_id SET NOT NULL;
ALTER TABLE service_accounts DROP CONSTRAINT service_accounts_pkey;
ALTER TABLE service_accounts ADD PRIMARY KEY(order_id);
CREATE INDEX service_customer ON service_accounts(customer_id,updated_at DESC);
CREATE UNIQUE INDEX service_external ON service_accounts(external_id);
ALTER TABLE plans ADD COLUMN quota_unit text NOT NULL DEFAULT 'GB' CHECK(quota_unit IN ('GB','GiB'));
CREATE TABLE telegram_confirmations(id text PRIMARY KEY,user_id bigint NOT NULL,administrator_id text NOT NULL REFERENCES customers(id),token_hash text NOT NULL,payment_id text NOT NULL REFERENCES manual_payments(id),payment_version integer NOT NULL,expires_at timestamptz NOT NULL,used_at timestamptz);
CREATE INDEX telegram_confirm_expiry ON telegram_confirmations(expires_at);
ALTER TABLE orders ADD COLUMN per_order boolean NOT NULL DEFAULT false;
ALTER TABLE orders ALTER COLUMN per_order SET DEFAULT true;
ALTER TABLE orders ADD COLUMN fulfillment_payload bytea;
CREATE TABLE service_migrations(customer_id text PRIMARY KEY REFERENCES customers(id),legacy_external_id text NOT NULL UNIQUE,payload bytea NOT NULL,response jsonb,state text NOT NULL DEFAULT 'queued',created_at timestamptz NOT NULL DEFAULT now(),applied_at timestamptz);
ALTER TABLE jobs DROP CONSTRAINT jobs_kind_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_kind_check CHECK(kind IN ('fulfill','mail','access','update','gateway','migrate'));
INSERT INTO jobs(id,kind,subject) SELECT md5('split/'||customer_id),'migrate',customer_id FROM service_accounts WHERE legacy_combined ON CONFLICT(kind,subject) DO NOTHING;
UPDATE plans SET quota_bytes=round(quota_bytes::numeric*1073741824/1000000000)::bigint,quota_unit='GiB',version=version+1 WHERE quota_unit='GB';
UPDATE orders SET per_order=true,snapshot=jsonb_set(jsonb_set(snapshot,'{plan,quota_bytes}',to_jsonb(round((snapshot->'plan'->>'quota_bytes')::numeric*1073741824/1000000000)::bigint)),'{plan,quota_unit}','"GiB"') WHERE state='pending' AND fulfillment='none' AND COALESCE(snapshot->'plan'->>'quota_unit','GB')='GB';
INSERT INTO announcements(id,title,body) VALUES
 ('a4000000000000000000000000000001','购买与订阅说明','新购买的每个订单单独开通身份、流量与订阅。同一套餐购买多次也分别管理。旧合并权益迁移后旧链接失效，请在“订阅与设备”分别复制各订单链接并更新客户端。'),
 ('a4000000000000000000000000000002','流量单位与使用量','套餐统一以 GiB 标记，1 GiB = 1024³ 字节，与 Clash 的常用显示口径一致。已用量包含本期计费流量；剩余量随节点回传更新，过期或额度耗尽后服务会受限。'),
 ('a4000000000000000000000000000003','付款与客户端帮助','口令红包提交后需要管理员领取并确认实际到账；静态收款码请提交流水或截图。请在“客户端教程”选择 Android、iOS、Windows 等平台。订阅链接和二维码包含访问凭据，请勿公开。') ON CONFLICT(id) DO NOTHING;
