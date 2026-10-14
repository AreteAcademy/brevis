select day, sku, revenue
from marts.orders_daily
where day between {{ start_date }} and {{ end_date }}
{% if sku %}and sku = {{ sku }}{% endif %}
order by sku, day
