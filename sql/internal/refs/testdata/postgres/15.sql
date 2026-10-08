-- hand-written: derived tables nested, and USING
select *
from (select * from (select * from ledger.entries where amount <> 0) a) b
join ledger.accounts using (account_id)
