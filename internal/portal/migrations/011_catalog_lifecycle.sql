ALTER TABLE plans ADD COLUMN deleted_at timestamptz;
ALTER TABLE plans ADD COLUMN deleted_by text;
ALTER TABLE service_accounts ADD COLUMN hidden_at timestamptz;
ALTER TABLE orders ADD COLUMN renewal_deadline_at timestamptz;
CREATE INDEX plans_visible ON plans(category_id,sort) WHERE deleted_at IS NULL;
CREATE INDEX orders_plan_inventory ON orders(plan_id,state) WHERE state IN ('pending','paid','refund_pending');
-- Examples with no bound groups must not advertise unselected protocols.
UPDATE plans SET protocols='[]',version=version+1 WHERE simulation AND group_ids='[]' AND protocols='["anytls", "hysteria2"]'::jsonb;
-- Preserve existing confirmed entitlements. Bound only outstanding renewal invoices.
UPDATE orders o SET renewal_deadline_at=(s.remote_summary->>'expires_at')::timestamptz+interval '72 hours'
FROM service_accounts s WHERE o.renewal_of=s.order_id AND o.state='pending' AND o.renewal_deadline_at IS NULL AND s.remote_summary->>'expires_at' IS NOT NULL;
UPDATE orders SET expires_at=LEAST(expires_at,renewal_deadline_at) WHERE renewal_of IS NOT NULL AND state='pending' AND renewal_deadline_at IS NOT NULL;
