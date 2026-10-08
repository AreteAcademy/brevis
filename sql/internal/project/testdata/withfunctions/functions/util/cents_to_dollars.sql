create or replace function util.cents_to_dollars(cents numeric) returns numeric
language sql immutable
as $$ select (cents::numeric(16, 2) / 100) $$;
