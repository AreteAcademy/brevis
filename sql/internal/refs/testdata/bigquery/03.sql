-- QUALIFY to keep the latest row per key
select *
from `acme-prod.bronze.clicks`
where true
qualify row_number() over (partition by event_id order by received_at desc) = 1
