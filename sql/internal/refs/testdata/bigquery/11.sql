-- region-level INFORMATION_SCHEMA
select user_email, sum(total_bytes_billed) as bytes
from `region-us`.INFORMATION_SCHEMA.JOBS_BY_PROJECT
where creation_time > timestamp_sub(current_timestamp(), interval 7 day)
group by 1
