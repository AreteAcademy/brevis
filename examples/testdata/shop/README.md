# A small shop, in three CSVs

Fixture data for the examples: enough to join, group and aggregate over, small
enough to read by hand and to keep in the repository.

| file | rows | |
|---|---|---|
| `clients.csv` | 120 | id, name, email, state, tier, joined_at |
| `products.csv` | 350 | id, sku, name, category, unit_price, in_stock |
| `orders.csv` | 2,000 | id, client_id, product_id, quantity, unit_price, discount, total, status, placed_at |

Every order points at a client and a product that exist — checked, not assumed.

## Why it is not uniform

A flat random dataset makes every aggregation come out the same, and an example
built on one teaches nothing: the top customer is whoever the shuffle picked, and
a `GROUP BY` looks identical to a `LIMIT`.

So the shape is deliberate:

- **Twelve of the 120 clients place a third of the orders.** A ranking has a
  real head and a real tail.
- **Quantities are skewed** — most orders are one or two units, a few are ten.
- **Some orders carry a discount.** So `SUM(total)` and
  `SUM(quantity * unit_price)` genuinely differ, which is what makes a
  reconciliation query worth writing.
- **Four statuses, unevenly**: about half `paid`, a quarter `shipped`, and the
  rest `refunded` or `cancelled`. Revenue means `paid + shipped`, so any example
  that forgets the filter is visibly wrong rather than plausibly wrong.

Generated with a fixed seed, so the files are identical on every machine and a
diff means somebody changed them on purpose.
