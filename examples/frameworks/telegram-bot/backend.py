"""Telegram is only a channel. The webhook backend, not the bot update, grants an order."""
import hashlib
MAX_BODY=1_048_576

def merchant_webhook(raw_body,headers,verify_webhook,resolve_secret,db):
    if not raw_body or len(raw_body)>MAX_BODY:return 413,{"error":"invalid_body"}
    verified=verify_webhook(raw_body,headers["Merchant-Webhook-Signature"],headers["Content-Digest"],resolve_secret);event=verified.event;digest=hashlib.sha256(raw_body).hexdigest()
    with db.transaction() as tx:
        existing=tx.one("SELECT body_sha256 FROM merchant_webhook_inbox WHERE event_id=%s FOR UPDATE",[verified.event_id])
        if existing:return (200,{"acknowledged_event_id":verified.event_id}) if existing["body_sha256"]==digest else (409,{"error":"event_digest_conflict"})
        tx.exec("INSERT INTO merchant_webhook_inbox(event_id,body_sha256,event_type) VALUES(%s,%s,%s)",[verified.event_id,digest,event.event_type])
        if event.event_type=="payment.settled":
            intent=event.payment_intent;order=tx.one("SELECT * FROM commerce_orders WHERE merchant_order_id=%s FOR UPDATE",[intent["merchant_order_id"]])
            if not order or order["expected_amount_minor"]!=intent["amount_minor"] or order["expected_currency"]!=intent["currency"]:raise ValueError("settlement does not match local order")
            tx.exec("UPDATE commerce_orders SET state='paid',paid_event_id=%s WHERE id=%s AND state='awaiting_payment'",[verified.event_id,order["id"]])
            # A separate worker sends a Telegram message or grants any product. Network I/O never occurs in this transaction.
            tx.exec("INSERT INTO fulfillment_outbox(id,event_id,order_id,kind,payload) VALUES(gen_random_uuid(),%s,%s,'telegram_order_paid','{}') ON CONFLICT(event_id) DO NOTHING",[verified.event_id,order["id"]])
    return 200,{"acknowledged_event_id":verified.event_id}

def bot_create_payment(chat_id,cart,merchant_client,db):
    # chat_id stays in the client database; only an opaque customer_reference is sent to merchant.
    order=db.create_awaiting_order(chat_id,cart)
    return merchant_client.create_payment_intent(order.to_api_request(),f"order:{order.id}:create")
