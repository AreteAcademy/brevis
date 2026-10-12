select partition as day, sku, sum(quantity) as units, sum(quantity * price) as revenue
from sales.daily
{% if sku %}where sku = {{ sku }}{% endif %}
group by 1, 2
order by 1, 2
