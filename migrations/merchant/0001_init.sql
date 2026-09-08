-- merchant-service schema: merchants (the closed list of connected outlets)
-- and their access keys. Owned exclusively by merchant-service.

CREATE TABLE merchants (
    id                  uuid        PRIMARY KEY,
    slug                text        NOT NULL,
    name                text        NOT NULL,
    description         text        NOT NULL DEFAULT '',
    cuisine             text        NOT NULL DEFAULT '',
    city                text        NOT NULL,
    address             text        NOT NULL,
    phone               text        NOT NULL DEFAULT '',
    status              text        NOT NULL,
    is_accepting_orders boolean     NOT NULL DEFAULT false,
    min_order_minor     bigint      NOT NULL DEFAULT 0,
    delivery_fee_minor  bigint      NOT NULL DEFAULT 0,
    avg_prep_minutes    integer     NOT NULL DEFAULT 30,
    version             bigint      NOT NULL DEFAULT 1,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT merchants_slug_key UNIQUE (slug),
    CONSTRAINT merchants_status_chk CHECK (status IN ('ACTIVE', 'SUSPENDED')),
    CONSTRAINT merchants_name_chk CHECK (char_length(name) BETWEEN 1 AND 200),
    CONSTRAINT merchants_city_chk CHECK (char_length(city) BETWEEN 1 AND 100),
    CONSTRAINT merchants_address_chk CHECK (char_length(address) BETWEEN 1 AND 500),
    CONSTRAINT merchants_min_order_chk
        CHECK (min_order_minor >= 0 AND min_order_minor <= 10000000),
    CONSTRAINT merchants_delivery_fee_chk
        CHECK (delivery_fee_minor >= 0 AND delivery_fee_minor <= 1000000),
    CONSTRAINT merchants_prep_chk CHECK (avg_prep_minutes BETWEEN 1 AND 240),
    CONSTRAINT merchants_version_chk CHECK (version > 0)
);

COMMENT ON TABLE merchants IS
    'Outlets connected to the platform. Closed list maintained by the operator.';
COMMENT ON COLUMN merchants.status IS
    'Platform membership, changed by the operator only.';
COMMENT ON COLUMN merchants.is_accepting_orders IS
    'Operational switch owned by the merchant; independent of status.';
COMMENT ON COLUMN merchants.version IS
    'Incremented on every profile write; drives optimistic concurrency control.';

-- Serves the public list: active outlets of a city ordered by name.
CREATE INDEX merchants_active_city_name_idx
    ON merchants (city, name, id)
    WHERE status = 'ACTIVE';

CREATE TABLE merchant_api_keys (
    key_hash    bytea       PRIMARY KEY,
    merchant_id uuid        NOT NULL REFERENCES merchants (id) ON DELETE CASCADE,
    label       text        NOT NULL DEFAULT '',
    is_active   boolean     NOT NULL DEFAULT true,
    created_at  timestamptz NOT NULL DEFAULT now(),
    revoked_at  timestamptz,

    CONSTRAINT merchant_api_keys_hash_chk CHECK (octet_length(key_hash) = 32),
    CONSTRAINT merchant_api_keys_revocation_chk CHECK (
        (is_active AND revoked_at IS NULL) OR (NOT is_active AND revoked_at IS NOT NULL)
    )
);

COMMENT ON TABLE merchant_api_keys IS
    'SHA-256 digests of merchant access keys; raw keys are never stored.';

CREATE INDEX merchant_api_keys_merchant_idx ON merchant_api_keys (merchant_id);
