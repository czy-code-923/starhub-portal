ALTER TABLE orders ADD COLUMN renewal_of text REFERENCES orders(id);
CREATE INDEX orders_renewal ON orders(renewal_of,state) WHERE renewal_of IS NOT NULL;
CREATE INDEX orders_stock ON orders(plan_id,state) WHERE renewal_of IS NULL;
