-- order-service schema: orders, their item snapshots and the transactional
-- outbox that carries a terminal decision to inventory-service.
--
-- Owned exclusively by order-service (TZ v3.0 §6.4).
--
-- Two properties of this schema carry the correctness of the whole placement
-- flow: UNIQUE (customer_id, idempotency_key) makes a repeated placement
-- return the first order instead of creating a second one, and
-- outbox_messages is written in the same transaction as the status change, so
-- a decision cannot be recorded without its delivery being scheduled.

CREATE TABLE orders (
    id                 uuid        PRIMARY KEY,
    merchant_id        uuid        NOT NULL,
    -- Snapshot of the merchant name at placement time: reading an order must
    -- not require a call to merchant-service.
    merchant_name      text        NOT NULL,
    customer_id        uuid        NOT NULL,
    idempotency_key    text        NOT NULL,
    customer_name      text        NOT NULL,
    customer_phone     text        NOT NULL,
    delivery_address   text        NOT NULL,
    comment            text        NOT NULL DEFAULT '',
    status             text        NOT NULL,
    items_total_minor  bigint      NOT NULL,
    delivery_fee_minor bigint      NOT NULL,
    total_minor        bigint      NOT NULL,
    currency           text        NOT NULL DEFAULT 'RUB',
    eta_minutes        integer     NOT NULL DEFAULT 0,
    reject_reason      text        NOT NULL DEFAULT '',
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),

    -- Idempotency of placement: the scope is the customer, the lifetime is the
    -- lifetime of the order (TZ §8.2).
    CONSTRAINT orders_customer_idempotency_key UNIQUE (customer_id, idempotency_key),
    CONSTRAINT orders_status_chk CHECK (status IN
        ('CREATED', 'CONFIRMED', 'REJECTED', 'CANCELLED',
         'COOKING', 'DELIVERING', 'DELIVERED')),
    CONSTRAINT orders_totals_chk CHECK (total_minor = items_total_minor + delivery_fee_minor),
    CONSTRAINT orders_items_total_chk CHECK (items_total_minor > 0),
    CONSTRAINT orders_delivery_fee_chk CHECK (delivery_fee_minor >= 0),
    CONSTRAINT orders_currency_chk CHECK (currency = 'RUB'),
    CONSTRAINT orders_eta_chk CHECK (eta_minutes BETWEEN 0 AND 240),
    CONSTRAINT orders_idempotency_key_chk CHECK (char_length(idempotency_key) BETWEEN 1 AND 128),
    CONSTRAINT orders_customer_name_chk CHECK (char_length(customer_name) BETWEEN 1 AND 200),
    CONSTRAINT orders_phone_chk CHECK (char_length(customer_phone) BETWEEN 5 AND 32),
    CONSTRAINT orders_address_chk CHECK (char_length(delivery_address) BETWEEN 1 AND 500),
    CONSTRAINT orders_comment_chk CHECK (char_length(comment) <= 500),
    CONSTRAINT orders_reject_reason_chk CHECK (char_length(reject_reason) <= 200)
);

COMMENT ON TABLE orders IS
    'Customer orders and their lifecycle. The merchant name and the item prices
     are snapshots taken at placement time.';
COMMENT ON COLUMN orders.idempotency_key IS
    'Idempotency-Key header of the placement request; unique per customer.';

-- Serves the merchant queue: new orders of one outlet, oldest first.
CREATE INDEX orders_merchant_queue_idx ON orders (merchant_id, status, created_at);
-- Serves the customer history.
CREATE INDEX orders_customer_history_idx ON orders (customer_id, created_at DESC);

CREATE TABLE order_items (
    order_id         uuid    NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
    menu_item_id     uuid    NOT NULL,
    external_id      text    NOT NULL,
    name             text    NOT NULL,
    unit_price_minor bigint  NOT NULL,
    qty              integer NOT NULL,
    line_total_minor bigint  NOT NULL,

    CONSTRAINT order_items_pkey PRIMARY KEY (order_id, menu_item_id),
    CONSTRAINT order_items_qty_chk CHECK (qty BETWEEN 1 AND 99),
    CONSTRAINT order_items_price_chk CHECK (unit_price_minor > 0),
    CONSTRAINT order_items_line_total_chk CHECK (line_total_minor = unit_price_minor * qty)
);

COMMENT ON TABLE order_items IS
    'Snapshot of the name and price of every ordered position at placement
     time, so reading an order never calls catalog-service.';

-- Transactional outbox (TZ §8.3). A terminal decision about an order changes
-- the status and inserts the message in ONE transaction, which is what removes
-- the dual-write problem: either both happen or neither does.
CREATE TABLE outbox_messages (
    id             uuid        PRIMARY KEY,
    aggregate_id   uuid        NOT NULL,
    type           text        NOT NULL,
    payload        jsonb       NOT NULL,
    correlation_id text        NOT NULL DEFAULT '',
    state          text        NOT NULL DEFAULT 'PENDING',
    attempts       integer     NOT NULL DEFAULT 0,
    available_at   timestamptz NOT NULL DEFAULT now(),
    last_error     text        NOT NULL DEFAULT '',
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT outbox_messages_state_chk CHECK (state IN ('PENDING', 'SENT', 'FAILED')),
    CONSTRAINT outbox_messages_type_chk CHECK (type IN
        ('RESERVATION_COMMIT', 'RESERVATION_RELEASE')),
    CONSTRAINT outbox_messages_attempts_chk CHECK (attempts >= 0)
);

COMMENT ON TABLE outbox_messages IS
    'Terminal decisions waiting to reach inventory-service. Delivery is
     at-least-once; the receiver is idempotent, so the effect happens once.';

-- Serves the dispatcher: only messages that still have to be delivered.
CREATE INDEX outbox_messages_due_idx
    ON outbox_messages (available_at, id)
    WHERE state = 'PENDING';
-- Serves the diagnostics of one order.
CREATE INDEX outbox_messages_aggregate_idx ON outbox_messages (aggregate_id);
