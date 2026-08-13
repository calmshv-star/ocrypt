from django.db import migrations


class Migration(migrations.Migration):
    dependencies = [("api", "0133_showy_crypto_invoice")]

    operations = [
        migrations.RunSQL(
            sql="""
                CREATE TABLE api_ocrypt_webhook_inbox (
                    event_id varchar(128) PRIMARY KEY,
                    body_sha256 char(64) NOT NULL,
                    event_type varchar(64) NOT NULL,
                    event_sequence bigint NOT NULL,
                    payment_id uuid,
                    order_id varchar(128) NOT NULL DEFAULT '',
                    payload jsonb NOT NULL,
                    processing_status varchar(16) NOT NULL,
                    result jsonb NOT NULL DEFAULT '{}'::jsonb,
                    received_at timestamptz NOT NULL,
                    processed_at timestamptz,
                    CONSTRAINT api_ocrypt_webhook_status_check
                      CHECK (processing_status IN ('received','processed'))
                );
                CREATE UNIQUE INDEX api_ocrypt_webhook_sequence_uniq
                  ON api_ocrypt_webhook_inbox(event_sequence);
                CREATE INDEX api_ocrypt_webhook_payment_idx
                  ON api_ocrypt_webhook_inbox(payment_id, received_at DESC);
            """,
            reverse_sql="DROP TABLE IF EXISTS api_ocrypt_webhook_inbox;",
        )
    ]
