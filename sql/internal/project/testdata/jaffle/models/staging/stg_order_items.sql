/* brevis
tests:
  - not_null: [order_id]
  - not_null: [order_item_id]
  - relationships: {column: order_id, to: staging.stg_orders, field: order_id}
  - unique: [order_item_id]
*/
with

source as (

    select * from raw.raw_items

),

renamed as (

    select

        ----------  ids
        id as order_item_id,
        order_id,
        sku as product_id

    from source

)

select * from renamed
