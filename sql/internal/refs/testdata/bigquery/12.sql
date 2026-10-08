-- a generated array is not a table
select d as day, count(o.order_id) as orders
from unnest(generate_date_array('2026-09-01', '2026-09-30')) as d
left join `acme-prod.sales.orders` o on date(o.created_at) = d
group by 1
