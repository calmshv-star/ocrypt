BEGIN;

-- The daily totals report scans paid intents of one tenant from a settlement
-- time onwards; without this index it filtered every paid intent the tenant
-- ever had.
CREATE INDEX payment_intents_paid_day_idx
  ON payment_intents (tenant_id, settled_at)
  INCLUDE (merchant_id, currency, currency_scale, amount_minor)
  WHERE status IN ('settled','overpaid');

COMMIT;
