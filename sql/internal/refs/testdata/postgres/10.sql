-- hand-written: LATERAL
select c.id, recent.*
from crm.customers c
cross join lateral (
    select o.id, o.total from sales.orders o
    where o.customer_id = c.id
    order by o.created_at desc
    limit 3
) recent
