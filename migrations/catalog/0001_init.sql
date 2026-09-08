-- catalog-service schema: what a merchant sells and for how much.
--
-- The catalogue owns menu structure, item identity and prices (TZ v3.0 §6.2).
-- It deliberately owns no quantity and no availability: how many units exist
-- belongs to inventory-service. Publishing a menu therefore never creates or
-- confirms stock.
--
-- Owned exclusively by catalog-service.

CREATE TABLE menu_categories (
    id          uuid    PRIMARY KEY,
    merchant_id uuid    NOT NULL,
    name        text    NOT NULL,
    position    integer NOT NULL DEFAULT 0,

    CONSTRAINT menu_categories_merchant_name_key UNIQUE (merchant_id, name),
    CONSTRAINT menu_categories_name_chk CHECK (char_length(name) BETWEEN 1 AND 100),
    CONSTRAINT menu_categories_position_chk CHECK (position >= 0)
);

COMMENT ON TABLE menu_categories IS
    'Sections of a merchant menu. A category is part of the published menu while
     it holds at least one item that is not withdrawn.';

CREATE TABLE menu_items (
    id          uuid        PRIMARY KEY,
    merchant_id uuid        NOT NULL,
    category_id uuid        NOT NULL REFERENCES menu_categories (id),
    external_id text        NOT NULL,
    name        text        NOT NULL,
    description text        NOT NULL DEFAULT '',
    price_minor bigint      NOT NULL,
    is_deleted  boolean     NOT NULL DEFAULT false,
    position    integer     NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),

    -- The identifier of the same item in the merchant's own system (A-10).
    -- It is what makes a published snapshot an upsert instead of a rewrite,
    -- so an item keeps its platform id across publications.
    CONSTRAINT menu_items_merchant_external_key UNIQUE (merchant_id, external_id),
    CONSTRAINT menu_items_external_id_chk CHECK (char_length(external_id) BETWEEN 1 AND 64),
    CONSTRAINT menu_items_name_chk CHECK (char_length(name) BETWEEN 1 AND 200),
    CONSTRAINT menu_items_description_chk CHECK (char_length(description) <= 1000),
    -- Money is stored as whole minor units; a sellable item costs something.
    CONSTRAINT menu_items_price_chk CHECK (price_minor > 0 AND price_minor <= 100000000),
    CONSTRAINT menu_items_position_chk CHECK (position >= 0)
);

COMMENT ON COLUMN menu_items.is_deleted IS
    'Withdrawn from the published menu. Rows are never removed physically:
     order snapshots and inventory stock cards reference this identifier.';

-- Serves the menu read path: the items of one merchant in display order.
CREATE INDEX menu_items_menu_idx
    ON menu_items (merchant_id, category_id, position)
    WHERE NOT is_deleted;

-- The version of the whole menu of one merchant. It lives in its own table
-- because catalog-service does not own the merchant entity and therefore has
-- no row of its own to keep the version in.
CREATE TABLE merchant_menu_state (
    merchant_id  uuid        PRIMARY KEY,
    menu_version bigint      NOT NULL,
    updated_at   timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT merchant_menu_state_version_chk CHECK (menu_version > 0)
);

COMMENT ON TABLE merchant_menu_state IS
    'Menu version of one merchant, incremented by every publication and by every
     item change; the value clients send back in If-Match.';
