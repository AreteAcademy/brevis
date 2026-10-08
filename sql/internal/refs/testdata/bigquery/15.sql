-- PIVOT over a derived table
select *
from (select region, month, revenue from `acme-prod.finance.revenue`)
pivot (sum(revenue) for month in ('jan', 'feb', 'mar'))
