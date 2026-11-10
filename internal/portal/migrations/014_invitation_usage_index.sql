-- Usage history is derived from the existing authoritative claims and orders.
-- No additional copies of customer data or unbounded event logs are introduced.
CREATE INDEX invitation_claims_history ON invitation_claims(invitation_id,customer_id);
