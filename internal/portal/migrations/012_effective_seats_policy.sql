ALTER TABLE plans ADD COLUMN policy_revision integer NOT NULL DEFAULT 1;
UPDATE plans SET policy_revision=version;
ALTER TABLE service_accounts ADD COLUMN policy_version integer NOT NULL DEFAULT 0;
ALTER TABLE service_accounts ADD COLUMN policy_error text NOT NULL DEFAULT '';
ALTER TABLE service_accounts ADD COLUMN policy_checked_at timestamptz;
ALTER TABLE jobs DROP CONSTRAINT jobs_kind_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_kind_check CHECK(kind IN ('fulfill','mail','access','update','gateway','migrate','policy'));

-- Each original order represents one independent service identity. Renewals of
-- that identity share its seat, including when an invoice spans its expiry.
CREATE FUNCTION portal_effective_seats(plan text,at_time timestamptz)
RETURNS TABLE(service_id text) LANGUAGE sql STABLE AS $$
 SELECT s.order_id FROM service_accounts s JOIN orders o ON o.id=s.order_id
 JOIN customers c ON c.id=s.customer_id
 WHERE s.plan_id=plan AND o.state='paid' AND c.deleted_at IS NULL AND c.status='active'
 AND (s.remote_state='unknown' OR (s.remote_state IN ('active','exhausted')
 AND (s.remote_summary->>'expires_at')::timestamptz>at_time))
 UNION
 SELECT COALESCE(o.renewal_of,o.id) FROM orders o WHERE o.plan_id=plan
 AND (o.state IN ('pending','refund_pending') OR (o.state='paid' AND o.fulfillment<>'active'))
$$;
CREATE INDEX service_accounts_plan_policy ON service_accounts(plan_id,policy_version);
INSERT INTO jobs(id,kind,subject)
SELECT md5('policy-upgrade/'||p.id),'policy',p.id FROM plans p
WHERE NOT p.simulation AND EXISTS(SELECT 1 FROM service_accounts s WHERE s.plan_id=p.id AND NOT s.legacy_combined)
ON CONFLICT(kind,subject) DO NOTHING;
