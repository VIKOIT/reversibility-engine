CREATE OR REPLACE VIEW active_orders AS SELECT * FROM orders WHERE status = 'active';
