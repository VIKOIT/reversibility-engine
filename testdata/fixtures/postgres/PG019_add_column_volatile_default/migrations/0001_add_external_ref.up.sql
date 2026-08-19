ALTER TABLE orders ADD COLUMN external_ref uuid DEFAULT gen_random_uuid();
