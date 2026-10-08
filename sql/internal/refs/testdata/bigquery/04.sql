-- SELECT * EXCEPT / REPLACE
select * except (raw_payload) replace (lower(email) as email)
from `acme-prod.crm.contacts`
