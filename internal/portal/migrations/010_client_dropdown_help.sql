-- Only replace the previous stock instructions; retain custom operator content.
UPDATE portal_content SET document=jsonb_set(document,'{faqs}',(
 SELECT jsonb_agg(CASE WHEN f->>'question'='不同客户端的订阅如何查看' AND f->>'answer' IN (
 '在“订阅与设备”选择具体订单，再在客户端卡片中查看、复制链接或显示二维码。不同格式共享此订单权益；不提供配置文件下载。',
 '在订阅与设备中选择具体订单，再在客户端卡片中查看、复制链接或显示二维码。不同格式共享此订单权益；不提供配置文件下载。')
 THEN '{"question":"不同客户端的订阅如何查看","answer":"在订阅与设备选择具体订阅，再用客户端订阅格式下拉框切换。复制对应链接或显示二维码后导入客户端；不提供配置文件下载。"}'::jsonb ELSE f END ORDER BY n)
 FROM jsonb_array_elements(document->'faqs') WITH ORDINALITY AS e(f,n)
)),version=version+1,updated_at=now() WHERE name='client-help' AND EXISTS(
 SELECT 1 FROM jsonb_array_elements(document->'faqs') f WHERE f->>'question'='不同客户端的订阅如何查看' AND f->>'answer' IN (
 '在“订阅与设备”选择具体订单，再在客户端卡片中查看、复制链接或显示二维码。不同格式共享此订单权益；不提供配置文件下载。',
 '在订阅与设备中选择具体订单，再在客户端卡片中查看、复制链接或显示二维码。不同格式共享此订单权益；不提供配置文件下载。'));
