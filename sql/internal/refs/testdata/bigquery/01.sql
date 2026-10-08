-- GA4 export shape: wildcard table, _TABLE_SUFFIX, scalar subquery over UNNEST
select
  user_pseudo_id,
  (select value.int_value from unnest(event_params) where key = 'ga_session_id') as session_id
from `analytics-prod.analytics_123456.events_*`
where _TABLE_SUFFIX between '20260901' and '20260930'
  and event_name = 'session_start'
