DELETE FROM sessions WHERE expires_at < now();
UPDATE orders SET status = 'archived' WHERE created_at < now() - interval '1 year';
