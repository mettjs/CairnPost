-- Whether an address is suppressed, and for which sending domains, is now
-- asked by the address alone. The primary key leads with the domain, so it
-- cannot answer that without reading every domain's rows.
CREATE INDEX suppressions_by_address ON suppressions (address) WHERE lifted_at IS NULL;
