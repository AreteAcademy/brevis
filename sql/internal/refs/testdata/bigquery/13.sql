-- subqueries in WHERE: EXISTS and IN
select c.*
from `acme-prod.crm.customers` c
where exists (select 1 from `acme-prod.sales.orders` o where o.customer_id = c.id)
  and c.country in (select code from `acme-prod.ref.countries` where active)
