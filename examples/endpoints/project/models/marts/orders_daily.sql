/* brevis
materialized: table
*/
-- One row per day and SKU, from the orders the gateway landed in
-- sales.events. The endpoint reads this, never the raw events: an API over a
-- model is one query plan the author chose, not one per caller.
select (placed_at at time zone 'UTC')::date as day,
       sku,
       count(*) as orders,
       sum(quantity) as units,
       sum(total) as revenue
from sales.events
group by 1, 2
