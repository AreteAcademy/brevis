-- hand-written: quoted identifiers that need their quotes
select oi."Quantity", c."Name"
from "Raw"."Order Items" oi
join "public"."Customers" c on c."Id" = oi."CustomerId"
