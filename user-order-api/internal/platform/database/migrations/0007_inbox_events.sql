CREATE TABLE inbox_events (
    consumer_name VARCHAR(100) NOT NULL,
    event_id CHAR(36) NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'processing',
    attempts INT UNSIGNED NOT NULL DEFAULT 0,
    locked_until DATETIME(6) NULL,
    processed_at DATETIME(6) NULL,
    last_error VARCHAR(1000) NULL,
    updated_at DATETIME(6) NOT NULL,
    PRIMARY KEY (consumer_name, event_id),
    INDEX idx_inbox_retry (consumer_name, status, locked_until)
);
