-- dataset-level INFORMATION_SCHEMA: a four-part name
select table_name, creation_time
from `acme-prod.bronze.INFORMATION_SCHEMA.TABLES`
where table_name like 'clicks%'
