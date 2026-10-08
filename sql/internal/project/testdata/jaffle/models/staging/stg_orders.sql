/* brevis
tests:
  - not_null: [order_id]
  - unique: [order_id]
*/
with

source as (

    select * from raw.raw_orders

),

renamed as (

    select

        ----------  ids
        id as order_id,
        store_id as location_id,
        customer as customer_id,

        ---------- numerics
        subtotal as subtotal_cents,
        tax_paid as tax_paid_cents,
        order_total as order_total_cents,
        util.cents_to_dollars(subtotal) as subtotal,
        util.cents_to_dollars(tax_paid) as tax_paid,
        util.cents_to_dollars(order_total) as order_total,

        ---------- timestamps
        date_trunc('day', ordered_at) as ordered_at

    from source

)

select * from renamed
