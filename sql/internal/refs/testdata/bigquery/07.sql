-- MERGE: the target is written, the source subquery is read
merge `acme-prod.silver.orders` t
using (select * from `acme-prod.bronze.orders` where _ingested_at > @since) s
on t.order_id = s.order_id
when matched then update set status = s.status
when not matched then insert row
