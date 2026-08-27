ALTER TABLE orders ADD CONSTRAINT orders_reference_key UNIQUE USING INDEX orders_reference_idx;
