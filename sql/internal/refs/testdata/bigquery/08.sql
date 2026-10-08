# BigQuery accepts hash comments: select * from fake.hash_comment
-- select * from fake.dash_comment
/* select * from fake.block_comment */
select 'select * from fake.in_a_string' as note, id
from `acme-prod.gold.real_table`
