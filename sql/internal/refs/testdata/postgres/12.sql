-- hand-written: comments and strings that contain FROM
-- select * from fake.in_a_line_comment
/* select * from fake.in_a_block_comment */
select 'select * from fake.in_a_string' as note, x.id
from real_schema.real_table x
