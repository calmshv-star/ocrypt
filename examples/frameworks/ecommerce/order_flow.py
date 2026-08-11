"""Framework-neutral e-commerce order adapter. Payment and fulfillment remain separate."""
import hashlib
def accept_settlement(raw_body,headers,verifier,unit_of_work):
    if not raw_body or len(raw_body)>1_048_576:return 413,{"error":"invalid_body"}
    verified=verifier.verify(raw_body,headers);event=verified.event;digest=hashlib.sha256(raw_body).hexdigest()
    with unit_of_work.transaction() as tx:
        inbox=tx.query_one("SELECT body_sha256 FROM merchant_webhook_inbox WHERE event_id=? FOR UPDATE",verified.event_id)
        if inbox:
            if inbox.body_sha256!=digest:return 409,{"error":"event_digest_conflict"}
            return 200,{"acknowledged_event_id":verified.event_id}
        tx.execute("INSERT INTO merchant_webhook_inbox(event_id,body_sha256,event_type) VALUES(?,?,?)",verified.event_id,digest,event.event_type)
        if event.event_type=="payment.settled":
            intent=event.payment_intent;order=tx.query_one("SELECT * FROM commerce_orders WHERE merchant_order_id=? FOR UPDATE",intent["merchant_order_id"])
            if not order or order.expected_amount_minor!=intent["amount_minor"] or order.expected_currency!=intent["currency"]:raise ValueError("settlement does not match local order")
            tx.execute("UPDATE commerce_orders SET state='paid',paid_event_id=? WHERE id=? AND state='awaiting_payment'",verified.event_id,order.id)
            tx.execute("INSERT INTO fulfillment_outbox(id,event_id,order_id,kind,payload) VALUES(random_uuid(),?,?,?,?) ON CONFLICT(event_id) DO NOTHING",verified.event_id,order.id,"reserve_and_ship",{"order_id":str(order.id)})
    return 200,{"acknowledged_event_id":verified.event_id}

def fulfillment_worker(outbox,inventory,shipping):
    # Worker claims an outbox row with a lease; inventory and shipping use the outbox ID as their idempotency key.
    for job in outbox.claim_batch():
        inventory.reserve(job.payload["order_id"],idempotency_key=str(job.id));shipping.enqueue(job.payload["order_id"],idempotency_key=str(job.id));outbox.complete(job.id)
