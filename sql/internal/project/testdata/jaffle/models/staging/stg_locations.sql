/* brevis
tests:
  - not_null: [location_id]
  - unique: [location_id]
*/
with

source as (

    select * from raw.raw_stores

),

renamed as (

    select

        ----------  ids
        id as location_id,

        ---------- text
        name as location_name,

        ---------- numerics
        tax_rate,

        ---------- timestamps
        date_trunc('day', opened_at) as opened_date

    from source

)

select * from renamed
