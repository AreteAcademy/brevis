-- A NULL IN THE PARENT, on purpose. `relationships` written with NOT IN
-- instead of a LEFT JOIN passes silently whenever this column holds one --
-- `x NOT IN (…, NULL)` is unknown for every row, so the test finds nothing
-- and reports success exactly when the parent is the side with a problem.
-- Without this row that mutation survives.
SELECT 'c1' AS customer_id
UNION ALL SELECT 'c2'
UNION ALL SELECT NULLIF('c1', 'c1')
