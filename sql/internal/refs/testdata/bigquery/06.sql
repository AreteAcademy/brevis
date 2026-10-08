-- a CTE named like the table it reads
with clicks as (
  select * from `acme-prod.bronze.clicks` where event_name != 'heartbeat'
)
select s.session_id, count(*) as clicks
from clicks
join `acme-prod.silver.sessions` s using (session_id)
group by 1
