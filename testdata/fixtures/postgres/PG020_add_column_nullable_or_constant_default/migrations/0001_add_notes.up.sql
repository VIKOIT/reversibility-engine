ALTER TABLE orders ADD COLUMN notes text;
ALTER TABLE orders ADD COLUMN currency text DEFAULT 'USD';
