/* brevis
materialized: table
*/
with

supplies as (

    select * from staging.stg_supplies

)

select * from supplies
