ALTER TABLE service_accounts ADD COLUMN remote_state text NOT NULL DEFAULT 'unknown' CHECK(remote_state IN ('unknown','active','disabled','expired','exhausted','deleted','invalid'));
ALTER TABLE service_accounts ADD COLUMN remote_checked_at timestamptz;
ALTER TABLE service_accounts ADD COLUMN remote_summary jsonb NOT NULL DEFAULT '{}';
CREATE INDEX audit_retention ON audit(created_at,id);
CREATE INDEX jobs_history ON jobs(state,updated_at,id);

-- Replace only the stock download FAQ; preserve administrator-written help.
UPDATE portal_content SET document=jsonb_set(document,'{faqs}',(
 SELECT COALESCE(jsonb_agg(CASE WHEN f->>'question'='配置下载与订阅导入有什么区别' AND f->>'answer'='下载的是当前配置快照，不会自行更新。远程订阅链接可以在客户端更新节点；自动模式下的文件下载默认使用 Mihomo YAML。'
 THEN '{"question":"不同客户端的订阅如何查看","answer":"在订阅与设备中选择具体订单，再在客户端卡片中查看、复制链接或显示二维码。不同格式共享此订单权益；不提供配置文件下载。"}'::jsonb ELSE f END ORDER BY n),'[]'::jsonb)
 FROM jsonb_array_elements(document->'faqs') WITH ORDINALITY AS e(f,n)
)),version=version+1,updated_at=now() WHERE name='client-help' AND EXISTS(SELECT 1 FROM jsonb_array_elements(document->'faqs') f WHERE f->>'question'='配置下载与订阅导入有什么区别' AND f->>'answer'='下载的是当前配置快照，不会自行更新。远程订阅链接可以在客户端更新节点；自动模式下的文件下载默认使用 Mihomo YAML。');
