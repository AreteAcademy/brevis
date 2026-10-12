select day, sku, orders, units, revenue
from marts.orders_daily
where day between {{ start_date }} and {{ end_date }}
{% if sku %}and sku = {{ sku }}{% endif %}
order by day, sku
