CREATE TYPE order_status AS ENUM ('pending', 'shipped');
CREATE SEQUENCE legacy_id_seq;
CREATE EXTENSION pg_trgm;
