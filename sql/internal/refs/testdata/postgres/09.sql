-- hand-written: subqueries in WHERE
select i.*
from sales.invoices i
where exists (select 1 from sales.payments p where p.invoice_id = i.id)
  and i.customer_id not in (select customer_id from crm.blocked)
