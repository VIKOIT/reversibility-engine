WITH removed AS (DELETE FROM orders WHERE created_at < now() - interval '1 year' RETURNING *)
SELECT count(*) FROM removed;
SELECT setval('orders_id_seq', 1000);
SELECT count(*) FROM orders;
