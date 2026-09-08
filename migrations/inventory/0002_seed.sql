-- Development seed: stock cards of the demonstration merchant
-- "Кафе «Пример»" (11111111-1111-4111-8111-111111111111 in merchant-service).
--
-- The menu item identifiers below are the fixed demonstration ids of the
-- platform catalogue. catalog-service must reuse exactly these ids in its own
-- seed so that the local stack is consistent across services; the agreement is
-- recorded in docs/PROJECT-STATE.md.
--
-- The three cards deliberately cover the three states a customer can meet:
--   * sellable          - ENABLED with units on hand;
--   * enabled but empty - ENABLED with zero units, which proves that enabling
--                         sales does not confirm any quantity (TZ §5.3);
--   * stop-listed       - STOPPED although units are on hand.

INSERT INTO stock_items (
    id, merchant_id, menu_item_id, sales_state, on_hand_qty, reserved_qty
) VALUES
    ('b1111111-1111-4111-8111-000000000001',
     '11111111-1111-4111-8111-111111111111',
     'a1111111-1111-4111-8111-111111111111', 'ENABLED', 10, 0),
    ('b1111111-1111-4111-8111-000000000002',
     '11111111-1111-4111-8111-111111111111',
     'a1111111-1111-4111-8111-222222222222', 'ENABLED', 0, 0),
    ('b1111111-1111-4111-8111-000000000003',
     '11111111-1111-4111-8111-111111111111',
     'a1111111-1111-4111-8111-333333333333', 'STOPPED', 5, 0);

INSERT INTO stock_movements (
    stock_item_id, kind, delta_on_hand, delta_reserved,
    on_hand_after, reserved_after, reason
) VALUES
    ('b1111111-1111-4111-8111-000000000001', 'SET', 10, 0, 10, 0, 'seed'),
    ('b1111111-1111-4111-8111-000000000003', 'SET', 5, 0, 5, 0, 'seed');
