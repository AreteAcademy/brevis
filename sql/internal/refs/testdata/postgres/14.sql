-- hand-written: table functions are not tables
select d::date as day, null as tag from generate_series('2026-01-01'::date, '2026-01-31'::date, interval '1 day') d
union all
select e.created_at::date, t.tag
from events.web e
cross join unnest(e.tags) as t(tag)
