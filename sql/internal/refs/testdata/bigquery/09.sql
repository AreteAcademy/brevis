-- ARRAY subquery over a column's array: o.items is not a table
select o.id, array(select as struct sku, qty from unnest(o.items) where qty > 0) as lines
from `acme-prod.sales.orders` o
