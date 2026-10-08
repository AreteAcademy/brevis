/* brevis
materialized: table
*/
select d::date as date_day
from generate_series(date '2000-01-01', date '2029-12-31', interval '1 day') as d
