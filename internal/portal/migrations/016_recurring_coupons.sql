ALTER TABLE coupons ADD COLUMN recurrence text NOT NULL DEFAULT 'none' CHECK(recurrence IN ('none','until','forever'));
ALTER TABLE coupons ADD COLUMN recurring_until timestamptz;
ALTER TABLE coupons ADD CONSTRAINT recurring_coupon_expiry CHECK((recurrence='until')=(recurring_until IS NOT NULL));
ALTER TABLE orders ADD COLUMN coupon_reused boolean NOT NULL DEFAULT false;
ALTER TABLE orders ADD CONSTRAINT recurring_discount_is_renewal CHECK(NOT coupon_reused OR (renewal_of IS NOT NULL AND coupon_id IS NOT NULL));
