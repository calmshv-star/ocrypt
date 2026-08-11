"""FastAPI/Django adaptation points; provide framework DB and SDK verifier adapters."""
import hashlib
MAX_BODY=1_048_576

def process_raw_webhook(raw_body,headers,verify_webhook,resolve_secret,db):
    if not raw_body or len(raw_body)>MAX_BODY: return 413,{"error":"invalid_body"}
    verified=verify_webhook(raw_body,headers["Merchant-Webhook-Signature"],headers["Content-Digest"],resolve_secret)
    event=verified.event;digest=hashlib.sha256(raw_body).hexdigest()
    with db.transaction() as tx:
        existing=tx.fetch_one("SELECT body_sha256 FROM merchant_webhook_inbox WHERE event_id=%s FOR UPDATE",[verified.event_id])
        if existing:
            if existing["body_sha256"]!=digest: return 409,{"error":"event_digest_conflict"}
            return 200,{"acknowledged_event_id":verified.event_id}
        tx.execute("INSERT INTO merchant_webhook_inbox(event_id,body_sha256,event_type) VALUES(%s,%s,%s)",[verified.event_id,digest,event.event_type])
        if event.event_type=="payment.settled":
            intent=event.payment_intent;order=tx.fetch_one("SELECT * FROM commerce_orders WHERE merchant_order_id=%s FOR UPDATE",[intent["merchant_order_id"]])
            if not order or order["expected_amount_minor"]!=intent["amount_minor"] or order["expected_currency"]!=intent["currency"]: raise ValueError("settlement does not match local order")
            tx.execute("UPDATE commerce_orders SET state='paid',paid_event_id=%s WHERE id=%s AND state='awaiting_payment'",[verified.event_id,order["id"]])
            tx.execute("INSERT INTO fulfillment_outbox(id,event_id,order_id,kind,payload) VALUES(gen_random_uuid(),%s,%s,'fulfill_order','{}') ON CONFLICT(event_id) DO NOTHING",[verified.event_id,order["id"]])
    return 200,{"acknowledged_event_id":verified.event_id}

async def fastapi_endpoint(request,verify_webhook,resolve_secret,db):
    if int(request.headers.get("content-length","0"))>MAX_BODY:return 413,{"error":"invalid_body"}
    return process_raw_webhook(await request.body(),request.headers,verify_webhook,resolve_secret,db)

def django_view(request,verify_webhook,resolve_secret,db):
    # Configure DATA_UPLOAD_MAX_MEMORY_SIZE=MAX_BODY; request.body is the untouched bytes.
    return process_raw_webhook(request.body,request.headers,verify_webhook,resolve_secret,db)
