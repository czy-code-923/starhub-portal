INSERT INTO jobs(id,kind,subject)
SELECT md5('deleted-admin-access/'||c.id),'access',c.id FROM customers c
WHERE c.deleted_at IS NOT NULL AND c.role='admin' AND
 (EXISTS(SELECT 1 FROM service_accounts s WHERE s.customer_id=c.id) OR EXISTS(SELECT 1 FROM orders o WHERE o.customer_id=c.id AND o.state='paid'))
ON CONFLICT(kind,subject) DO UPDATE SET state='queued',attempts=0,available_at=now(),lease_until=NULL,lease_generation=NULL,detail='',updated_at=now();
