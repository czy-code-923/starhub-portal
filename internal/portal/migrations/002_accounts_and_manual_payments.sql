ALTER TABLE customers ADD COLUMN is_owner boolean NOT NULL DEFAULT false;
ALTER TABLE customers ADD COLUMN admin_scopes jsonb NOT NULL DEFAULT '[]';
ALTER TABLE customers ADD COLUMN admin_version integer NOT NULL DEFAULT 1;
UPDATE customers SET is_owner=true WHERE id=(SELECT id FROM customers WHERE role='admin' ORDER BY created_at,id LIMIT 1);
CREATE UNIQUE INDEX one_primary_administrator ON customers(is_owner) WHERE is_owner;
ALTER TABLE customers ADD CONSTRAINT owner_is_administrator CHECK(NOT is_owner OR role='admin');
ALTER TABLE invitations ADD COLUMN sealed_code bytea;
ALTER TABLE invitations ADD COLUMN version integer NOT NULL DEFAULT 1;
ALTER TABLE plans ADD COLUMN simulation boolean NOT NULL DEFAULT false;
ALTER TABLE email_tokens DROP CONSTRAINT email_tokens_purpose_check;
ALTER TABLE email_tokens ADD CONSTRAINT email_tokens_purpose_check CHECK(purpose IN ('verify','reset','change_email'));
ALTER TABLE email_tokens ADD COLUMN pending_email text NOT NULL DEFAULT '';

CREATE TABLE manual_payment_methods(
 id text PRIMARY KEY CHECK(id IN ('alipay_static','wechat_static')),
 provider text NOT NULL CHECK(provider IN ('alipay','wechat')),
 name text NOT NULL, instructions text NOT NULL DEFAULT '',
 enabled boolean NOT NULL DEFAULT false,
 qr_image bytea, qr_mime text NOT NULL DEFAULT '',
 version integer NOT NULL DEFAULT 1, updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO manual_payment_methods(id,provider,name) VALUES('alipay_static','alipay','支付宝个人收款码'),('wechat_static','wechat','微信个人收款码');
CREATE TABLE manual_payments(
 id text PRIMARY KEY, order_id text NOT NULL UNIQUE REFERENCES orders(id),
 method_id text NOT NULL REFERENCES manual_payment_methods(id), provider text NOT NULL,
 method_name text NOT NULL, instructions text NOT NULL, qr_image bytea NOT NULL, qr_mime text NOT NULL,
 state text NOT NULL DEFAULT 'awaiting' CHECK(state IN ('awaiting','submitted','approved','rejected','cancelled')),
 reference text NOT NULL DEFAULT '', payer_note text NOT NULL DEFAULT '',
 proof_image bytea, proof_mime text NOT NULL DEFAULT '',
 verified_reference text, received_amount bigint, received_at timestamptz,
 reviewed_by text REFERENCES customers(id), review_note text NOT NULL DEFAULT '', reviewed_at timestamptz,
 version integer NOT NULL DEFAULT 1, created_at timestamptz NOT NULL DEFAULT now(), submitted_at timestamptz,
 review_deadline timestamptz NOT NULL,
 CHECK(octet_length(qr_image)<=1048576),CHECK(proof_image IS NULL OR octet_length(proof_image)<=1048576)
);
CREATE UNIQUE INDEX manual_verified_transaction ON manual_payments(provider,verified_reference) WHERE state='approved';
CREATE INDEX manual_review_queue ON manual_payments(state,submitted_at);
