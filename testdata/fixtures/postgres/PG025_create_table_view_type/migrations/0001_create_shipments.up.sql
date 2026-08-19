CREATE TABLE shipments (id bigint PRIMARY KEY, order_id bigint NOT NULL);
CREATE VIEW active_shipments AS SELECT id FROM shipments WHERE order_id IS NOT NULL;
CREATE TYPE shipment_state AS ENUM ('pending', 'shipped');
