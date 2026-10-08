-- dataset.table with no project, and USING
select o.order_id, sum(i.amount) as total
from bronze.orders o
join bronze.order_items i using (order_id)
group by 1
