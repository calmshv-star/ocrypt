/** Adapt annotations to Spring MVC/WebFlux. Request body must be byte[], limited to 1 MiB. */
final class MerchantWebhookController {
  record Reply(int status,java.util.Map<String,String> body){}
  Reply accept(byte[] rawBody,java.util.Map<String,String> headers,Verifier verifier,Database db)throws Exception{
    if(rawBody.length==0||rawBody.length>1_048_576)return new Reply(413,java.util.Map.of("error","invalid_body"));
    var verified=verifier.verify(rawBody,headers.get("Merchant-Webhook-Signature"),headers.get("Content-Digest"));
    var digest=java.util.HexFormat.of().formatHex(java.security.MessageDigest.getInstance("SHA-256").digest(rawBody));
    int status=db.transaction(tx->{var existing=tx.one("SELECT body_sha256 FROM merchant_webhook_inbox WHERE event_id=? FOR UPDATE",verified.eventId());if(existing!=null)return existing.get("body_sha256").equals(digest)?200:409;
      tx.exec("INSERT INTO merchant_webhook_inbox(event_id,body_sha256,event_type) VALUES(?,?,?)",verified.eventId(),digest,verified.eventType());
      if(verified.eventType().equals("payment.settled")){var order=tx.one("SELECT * FROM commerce_orders WHERE merchant_order_id=? FOR UPDATE",verified.orderId());if(order==null||!order.get("expected_amount_minor").equals(verified.amountMinor())||!order.get("expected_currency").equals(verified.currency()))throw new IllegalStateException("settlement does not match local order");tx.exec("UPDATE commerce_orders SET state='paid',paid_event_id=? WHERE id=? AND state='awaiting_payment'",verified.eventId(),order.get("id"));tx.exec("INSERT INTO fulfillment_outbox(id,event_id,order_id,kind,payload) VALUES(gen_random_uuid(),?,?,'fulfill_order','{}') ON CONFLICT(event_id) DO NOTHING",verified.eventId(),order.get("id"));}return 200;});
    return status==409?new Reply(409,java.util.Map.of("error","event_digest_conflict")):new Reply(200,java.util.Map.of("acknowledged_event_id",verified.eventId()));
  }
  interface Verifier{Verified verify(byte[]raw,String signature,String digest)throws Exception;}interface Verified{String eventId();String eventType();String orderId();String amountMinor();String currency();}interface Database{int transaction(Work work)throws Exception;}interface Work{int run(Tx tx)throws Exception;}interface Tx{java.util.Map<String,String>one(String sql,Object...args);void exec(String sql,Object...args);}
}
