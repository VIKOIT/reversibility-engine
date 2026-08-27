CREATE MATERIALIZED VIEW order_totals AS SELECT status, count(*) FROM orders GROUP BY status;
