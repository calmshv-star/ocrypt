-- Apply with the application's migration tool. PostgreSQL syntax is shown.
CREATE TABLE commerce_orders (
  id uuid PRIMARY KEY,
  merchant_order_id text NOT NULL UNIQUE,
  expected_amount_minor numeric(78,0) NOT NULL CHECK (expected_amount_minor > 0),
  expected_currency char(3) NOT NULL,
  state text NOT NULL CHECK (state IN ('awaiting_payment','paid','fulfilled','cancelled','reorg_review')),
  paid_event_id text UNIQUE,
  updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE merchant_webhook_inbox (
  event_id text PRIMARY KEY CHECK (length(event_id) BETWEEN 1 AND 128),
  body_sha256 char(64) NOT NULL,
  event_type text NOT NULL,
  processed_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE fulfillment_outbox (
  id uuid PRIMARY KEY,
  event_id text NOT NULL UNIQUE REFERENCES merchant_webhook_inbox(event_id),
  order_id uuid NOT NULL REFERENCES commerce_orders(id),
  kind text NOT NULL,
  payload jsonb NOT NULL,
  state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','leased','completed','dead_letter')),
  created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
