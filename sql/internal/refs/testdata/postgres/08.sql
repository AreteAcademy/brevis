-- hand-written: a CTE named like the table it reads
with orders as (
    select * from analytics.orders where status <> 'void'
)
select o.*, c.name
from orders o
join public.customers c on c.id = o.customer_id
