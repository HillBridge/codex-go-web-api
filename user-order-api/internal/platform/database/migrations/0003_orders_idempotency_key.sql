ALTER TABLE orders
    ADD COLUMN idempotency_key VARCHAR(255) NULL,
    ADD CONSTRAINT uq_orders_idempotency_key UNIQUE (idempotency_key);
