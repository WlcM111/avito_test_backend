-- Development seed: the menu of the demonstration merchant
-- "Кафе «Пример»" (11111111-1111-4111-8111-111111111111 in merchant-service).
--
-- The three item identifiers below are the fixed demonstration ids of the
-- platform catalogue. They are the same ids that migrations/inventory/0002_seed.sql
-- keeps stock cards for, so the local stack is consistent across services:
--   * a1111111-…-111111111111 — ENABLED with units on hand      (sellable);
--   * a1111111-…-222222222222 — ENABLED with zero units         (not sellable);
--   * a1111111-…-333333333333 — STOPPED although units are on hand.
-- The agreement is recorded in docs/TZ-v3.0.md §14.1 and docs/PROJECT-STATE.md.
--
-- Publishing a menu creates no stock: the seeded prices and names say nothing
-- about how many units exist.

INSERT INTO menu_categories (id, merchant_id, name, position) VALUES
    ('c1111111-1111-4111-8111-000000000001',
     '11111111-1111-4111-8111-111111111111', 'Горячее', 0),
    ('c1111111-1111-4111-8111-000000000002',
     '11111111-1111-4111-8111-111111111111', 'Напитки', 1);

INSERT INTO menu_items (
    id, merchant_id, category_id, external_id, name, description,
    price_minor, is_deleted, position
) VALUES
    ('a1111111-1111-4111-8111-111111111111',
     '11111111-1111-4111-8111-111111111111',
     'c1111111-1111-4111-8111-000000000001',
     'POS-001', 'Борщ с говядиной',
     'Классический борщ со сметаной и пампушкой.', 42000, false, 0),
    ('a1111111-1111-4111-8111-222222222222',
     '11111111-1111-4111-8111-111111111111',
     'c1111111-1111-4111-8111-000000000001',
     'POS-002', 'Котлета по-киевски',
     'Подаётся с картофельным пюре.', 56000, false, 1),
    ('a1111111-1111-4111-8111-333333333333',
     '11111111-1111-4111-8111-111111111111',
     'c1111111-1111-4111-8111-000000000002',
     'POS-003', 'Морс клюквенный',
     'Домашний морс, 400 мл.', 15000, false, 0);

INSERT INTO merchant_menu_state (merchant_id, menu_version) VALUES
    ('11111111-1111-4111-8111-111111111111', 1);
