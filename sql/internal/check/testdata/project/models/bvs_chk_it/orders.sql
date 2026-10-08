/* brevis
tests:
  - not_null: [order_id]
  - unique: [order_id]
  - accepted_values: {column: status, values: [placed, shipped]}
  - relationships: {column: customer_id, to: bvs_chk_it.parents, field: customer_id}
*/
-- One planted violation of each kind, and nothing else.
--
-- NULLIF('A','A') rather than a bare NULL: an untyped NULL in a UNION is a
-- type-inference question each warehouse answers its own way, and this
-- fixture has to be the same text on both.
SELECT 'A' AS order_id, 'placed' AS status, 'c1' AS customer_id
UNION ALL SELECT 'A', 'shipped', 'c2'
UNION ALL SELECT NULLIF('A', 'A'), 'placed', 'c1'
UNION ALL SELECT 'B', 'lost', 'c1'
UNION ALL SELECT 'C', 'placed', 'c9'
