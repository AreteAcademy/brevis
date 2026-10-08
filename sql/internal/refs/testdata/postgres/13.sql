-- hand-written: MERGE (Postgres 15+); the target is written, not read
merge into dw.dim_customer t
using staging.customers s on t.customer_id = s.id
when matched then update set name = s.name
when not matched then insert (customer_id, name) values (s.id, s.name)
