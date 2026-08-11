import { createHash } from "node:crypto";
/** Configure express.raw({type:"application/json",limit:"1mb"}); never JSON middleware first. */
export async function merchantWebhook(req:any,res:any,{verifyWebhook,resolveWebhookSecret,db}:any){
  const rawBody:Buffer=req.body;if(!Buffer.isBuffer(rawBody)||rawBody.length===0||rawBody.length>1_048_576)return res.status(413).json({error:"invalid_body"});
  const verified=await verifyWebhook({rawBody,signatureHeader:req.get("Merchant-Webhook-Signature"),contentDigest:req.get("Content-Digest"),resolveSecret:resolveWebhookSecret});const digest=createHash("sha256").update(rawBody).digest("hex");
  const status=await db.transaction(async(tx:any)=>{
    const existing=await tx.oneOrNone("SELECT body_sha256 FROM merchant_webhook_inbox WHERE event_id=$1 FOR UPDATE",[verified.eventId]);
    if(existing){if(existing.body_sha256!==digest)return 409;return 200;}
    await tx.none("INSERT INTO merchant_webhook_inbox(event_id,body_sha256,event_type) VALUES($1,$2,$3)",[verified.eventId,digest,verified.event.event_type]);
    if(verified.event.event_type==="payment.settled"){
      const order=await tx.oneOrNone("SELECT * FROM commerce_orders WHERE merchant_order_id=$1 FOR UPDATE",[verified.event.payment_intent.merchant_order_id]);
      if(!order||order.expected_amount_minor!==verified.event.payment_intent.amount_minor||order.expected_currency!==verified.event.payment_intent.currency)throw new Error("settlement does not match local order");
      await tx.none("UPDATE commerce_orders SET state='paid',paid_event_id=$1 WHERE id=$2 AND state='awaiting_payment'",[verified.eventId,order.id]);
      await tx.none("INSERT INTO fulfillment_outbox(id,event_id,order_id,kind,payload) VALUES(gen_random_uuid(),$1,$2,'fulfill_order','{}') ON CONFLICT(event_id) DO NOTHING",[verified.eventId,order.id]);
    }return 200;
  });
  if(status===409)return res.status(409).json({error:"event_digest_conflict"});return res.status(200).json({acknowledged_event_id:verified.eventId});
}
