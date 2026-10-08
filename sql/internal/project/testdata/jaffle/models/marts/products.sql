/* brevis
materialized: table
*/
with

products as (

    select * from staging.stg_products

)

select * from products
