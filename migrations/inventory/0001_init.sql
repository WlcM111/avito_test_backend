-- inventory-service schema: per-unit availability of menu items.
--
-- Accounting model (TZ v3.0 §5): the unit of accounting is ONE sellable unit
-- of a menu item at a merchant. Units are fungible and counted; an individual
-- unit is not identified. The accounting object is the pair
-- (merchant_id, menu_item_id) and carries two independent dimensions:
--   * sales_state - the merchant's stop-list switch;
--   * quantities  - on_hand_qty and reserved_qty, in whole units.
-- Owned exclusively by inventory-service.

CREATE TABLE stock_items (
    id            uuid        PRIMARY KEY,
    merchant_id   uuid        NOT NULL,
    menu_item_id  uuid        NOT NULL,
    sales_state   text        NOT NULL DEFAULT 'ENABLED',
    on_hand_qty   integer     NOT NULL DEFAULT 0,
    reserved_qty  integer     NOT NULL DEFAULT 0,
    version       bigint      NOT NULL DEFAULT 1,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT stock_items_merchant_item_key UNIQUE (merchant_id, menu_item_id),
    CONSTRAINT stock_items_sales_state_chk CHECK (sales_state IN ('ENABLED', 'STOPPED')),
    -- INV-S1
    CONSTRAINT stock_items_on_hand_chk CHECK (on_hand_qty >= 0),
    -- INV-S2
    CONSTRAINT stock_items_reserved_chk CHECK (reserved_qty >= 0),
    -- INV-S3: no operation may hold more units than are physically present.
    CONSTRAINT stock_items_reserved_le_on_hand_chk CHECK (reserved_qty <= on_hand_qty),
    CONSTRAINT stock_items_on_hand_max_chk CHECK (on_hand_qty <= 1000000),
    CONSTRAINT stock_items_version_chk CHECK (version > 0)
);

COMMENT ON TABLE stock_items IS
    'Per-unit stock card of one menu item at one merchant.';
COMMENT ON COLUMN stock_items.sales_state IS
    'Stop-list switch. Independent of quantities: enabling sales never confirms units.';
COMMENT ON COLUMN stock_items.on_hand_qty IS
    'Units physically available for sale. Changed by the merchant only.';
COMMENT ON COLUMN stock_items.reserved_qty IS
    'Units held for unconfirmed orders. Changed by the platform only.';
COMMENT ON COLUMN stock_items.version IS
    'Incremented on every change; drives optimistic concurrency control.';

CREATE TABLE stock_reservations (
    id          uuid        PRIMARY KEY,
    merchant_id uuid        NOT NULL,
    state       text        NOT NULL,
    expires_at  timestamptz NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT stock_reservations_state_chk
        CHECK (state IN ('HELD', 'COMMITTED', 'RELEASED'))
);

COMMENT ON TABLE stock_reservations IS
    'Units held for one order. The primary key equals the order id, which makes
     Reserve idempotent (INV-S6).';

-- Serves the sweeper: only rows that can still expire are indexed.
CREATE INDEX stock_reservations_expiring_idx
    ON stock_reservations (expires_at)
    WHERE state = 'HELD';

CREATE TABLE stock_reservation_lines (
    reservation_id uuid    NOT NULL REFERENCES stock_reservations (id) ON DELETE CASCADE,
    stock_item_id  uuid    NOT NULL REFERENCES stock_items (id),
    menu_item_id   uuid    NOT NULL,
    qty            integer NOT NULL,

    CONSTRAINT stock_reservation_lines_pkey PRIMARY KEY (reservation_id, stock_item_id),
    -- INV-S10
    CONSTRAINT stock_reservation_lines_qty_chk CHECK (qty > 0)
);

CREATE INDEX stock_reservation_lines_stock_item_idx
    ON stock_reservation_lines (stock_item_id);

-- Append-only journal. Every change of a quantity writes one row in the same
-- transaction (INV-S9), so the history of each unit count is reconstructible
-- without identifying individual units.
CREATE TABLE stock_movements (
    id             bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    stock_item_id  uuid        NOT NULL REFERENCES stock_items (id),
    kind           text        NOT NULL,
    delta_on_hand  integer     NOT NULL,
    delta_reserved integer     NOT NULL,
    on_hand_after  integer     NOT NULL,
    reserved_after integer     NOT NULL,
    reservation_id uuid,
    operation_key  text,
    reason         text        NOT NULL DEFAULT '',
    created_at     timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT stock_movements_kind_chk
        CHECK (kind IN ('SET', 'ADJUST', 'RESERVE', 'RELEASE', 'COMMIT', 'EXPIRE')),
    CONSTRAINT stock_movements_after_chk
        CHECK (on_hand_after >= 0 AND reserved_after >= 0)
);

CREATE INDEX stock_movements_item_idx ON stock_movements (stock_item_id, id DESC);

-- Deduplication of AdjustOnHand (INV-S7): a relative change is not idempotent
-- by nature, so the caller supplies an operation key and a repeat returns the
-- stored result instead of applying the delta twice.
CREATE TABLE stock_operations (
    stock_item_id uuid        NOT NULL REFERENCES stock_items (id),
    operation_key text        NOT NULL,
    result        jsonb       NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT stock_operations_pkey PRIMARY KEY (stock_item_id, operation_key),
    CONSTRAINT stock_operations_key_chk CHECK (char_length(operation_key) BETWEEN 1 AND 128)
);
