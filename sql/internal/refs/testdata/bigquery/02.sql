-- GA4: daily and intraday tables together
select event_name, count(*) as n from `analytics-prod.analytics_123456.events_*` group by 1
union all
select event_name, count(*) from `analytics-prod.analytics_123456.events_intraday_*` group by 1
