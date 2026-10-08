-- correlated array path after a comma: e.items is a column, not a table
select e.event_id, item.item_id, e.device.category
from `analytics-prod.analytics_123456.events_20260930` e, e.items as item
