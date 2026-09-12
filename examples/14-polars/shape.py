"""Step two: Polars does the part Go should not.

Two thousand orders become one row per client -- a group-by over the whole set,
which is exactly the shape a per-record Transformer cannot express and a
dataframe expresses in six lines.

Nothing here is a Brevis API except the two context calls at the edges: in the
middle this is ordinary Polars, which is the point. It can be run by hand
against a staged file, with an editor and a linter that understand it.
"""

import os
import sys

import polars as pl
from brevis import context

# Where step one left the records. The path is read from the context rather
# than agreed by convention, so renaming the staging directory breaks in one
# place instead of two.
staged = context.get("extract.path")
if not staged:
    sys.exit("extract published no path: did step one actually load anything?")

# Every column is a string, and that is not an accident to work around: the CSV
# reader keeps what the file said instead of guessing types. The cast belongs
# here, where the schema is declared once and a bad value fails loudly.
orders = pl.read_ndjson(staged).with_columns(
    pl.col("quantity").cast(pl.Int64),
    pl.col("total").cast(pl.Float64),
)

# Revenue is paid + shipped. Cancelled and refunded orders are real rows with
# real totals, and summing them is the mistake this dataset was shaped to
# expose -- a quarter of the file is not revenue.
earned = orders.filter(pl.col("status").is_in(["paid", "shipped"]))

by_client = (
    earned.group_by("client_id")
    .agg(
        pl.len().alias("orders"),
        pl.col("quantity").sum().alias("units"),
        pl.col("total").sum().round(2).alias("revenue"),
    )
    .with_columns(
        (pl.col("revenue") / pl.col("orders")).round(2).alias("avg_ticket"),
    )
    .sort("revenue", descending=True)
)

# The env says WHERE the shared volume is; the context says WHICH file is in
# it. Writing beside the staged parts instead would put the aggregate inside
# the directory step one globs, which works until somebody re-runs.
out = os.path.join(os.environ["SHOP_WORKDIR"], "by_client.ndjson")
by_client.write_ndjson(out)

context.set(path=out, clients=by_client.height)

# stderr, not stdout: stdout carries the @brevis: protocol, and a stray line
# there is a parse error rather than a log.
print(
    f"{orders.height} orders -> {by_client.height} clients "
    f"(R$ {by_client['revenue'].sum():,.2f} earned)",
    file=sys.stderr,
)
