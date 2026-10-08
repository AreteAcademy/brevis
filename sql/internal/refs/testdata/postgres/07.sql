-- source: dbt-labs/jaffle-shop staging/stg_customers.sql, compiled by dbt-core 1.12.5 + dbt-postgres
with

source as (

    select * from "brevis_it"."raw"."raw_customers"

),

renamed as (

    select

        ----------  ids
        id as customer_id,

        ---------- text
        name as customer_name

    from source

)

select * from renamed