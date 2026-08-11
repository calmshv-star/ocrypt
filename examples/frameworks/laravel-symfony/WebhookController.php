<?php
/** Laravel: use DB::transaction. Symfony: use Connection::transactional. Disable JSON decoding before this controller. */
final class MerchantWebhookController {
  public function __invoke($request,$verifier,$resolveSecret,$db){
    $rawBody=$request->getContent();if($rawBody===''||strlen($rawBody)>1048576)return $this->json(413,['error'=>'invalid_body']);
    $verified=$verifier->verify($rawBody,$request->headers->get('Merchant-Webhook-Signature'),$request->headers->get('Content-Digest'),$resolveSecret);
    $digest=hash('sha256',$rawBody);$status=$db->transaction(function($tx)use($verified,$digest){
      $existing=$tx->fetchAssociative('SELECT body_sha256 FROM merchant_webhook_inbox WHERE event_id=? FOR UPDATE',[$verified->eventId]);
      if($existing){return hash_equals($existing['body_sha256'],$digest)?200:409;}
      $event=$verified->event->data;$tx->executeStatement('INSERT INTO merchant_webhook_inbox(event_id,body_sha256,event_type) VALUES(?,?,?)',[$verified->eventId,$digest,$event['event_type']]);
      if($event['event_type']==='payment.settled'){
        $intent=$event['payment_intent'];$order=$tx->fetchAssociative('SELECT * FROM commerce_orders WHERE merchant_order_id=? FOR UPDATE',[$intent['merchant_order_id']]);
        if(!$order||$order['expected_amount_minor']!==$intent['amount_minor']||$order['expected_currency']!==$intent['currency'])throw new \RuntimeException('settlement does not match local order');
        $tx->executeStatement("UPDATE commerce_orders SET state='paid',paid_event_id=? WHERE id=? AND state='awaiting_payment'",[$verified->eventId,$order['id']]);
        $tx->executeStatement("INSERT INTO fulfillment_outbox(id,event_id,order_id,kind,payload) VALUES(gen_random_uuid(),?,?,'fulfill_order','{}') ON CONFLICT(event_id) DO NOTHING",[$verified->eventId,$order['id']]);
      }return 200;
    });
    return $status===409?$this->json(409,['error'=>'event_digest_conflict']):$this->json(200,['acknowledged_event_id'=>$verified->eventId]);
  }
  private function json(int $status,array $body){return ['status'=>$status,'json'=>$body];}
}
