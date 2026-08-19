CREATE VIEW order_summary AS SELECT id, total FROM orders;
CREATE FUNCTION calculate_total(n numeric) RETURNS numeric AS $$ SELECT n $$ LANGUAGE sql;
CREATE TRIGGER orders_audit AFTER INSERT ON orders FOR EACH ROW EXECUTE FUNCTION audit_orders();
