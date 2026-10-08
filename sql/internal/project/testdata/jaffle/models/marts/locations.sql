/* brevis
materialized: table
*/
with

locations as (

    select * from staging.stg_locations

)

select * from locations
