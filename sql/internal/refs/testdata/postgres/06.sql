-- source: dbt-labs/jaffle-shop marts/locations.sql, compiled by dbt-core 1.12.5 + dbt-postgres
with

locations as (

    select * from "brevis_it"."jaffle"."stg_locations"

)

select * from locations