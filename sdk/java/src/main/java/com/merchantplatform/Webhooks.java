package com.merchantplatform;

import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.PropertyNamingStrategies;
import javax.crypto.Mac;
import javax.crypto.spec.SecretKeySpec;
import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.time.Instant;
import java.util.HashMap;
import java.util.Map;
import java.util.Optional;

public final class Webhooks {
    private static final ObjectMapper JSON=new ObjectMapper().setPropertyNamingStrategy(PropertyNamingStrategies.SNAKE_CASE);
    private Webhooks() {}
    public interface SecretResolver { Optional<String> resolve(String keyId); }
    public interface TransactionHandler<T> { void run(T transaction) throws Exception; }
    public interface WebhookInbox<T> { InboxResult process(String eventId,String bodyDigest,TransactionHandler<T> handler) throws Exception; }
    public enum InboxResult { PROCESSED, DUPLICATE, CONFLICT }
    public record VerifiedWebhook(Models.WebhookEvent event,String eventId,String keyId,long timestamp,String bodyDigest) {}
    public static final class VerificationException extends Exception { public VerificationException(String message){super(message);} }
    public static VerifiedWebhook verify(byte[] rawBody,String signatureHeader,String contentDigest,SecretResolver resolver,Instant now,long toleranceSeconds) throws VerificationException {
        Map<String,String> parts=new HashMap<>();for(String part:signatureHeader.split(",")){String[] pair=part.trim().split("=",2);if(pair.length==2)parts.put(pair[0],pair[1]);}long timestamp;try{timestamp=Long.parseLong(parts.get("t"));}catch(Exception error){throw new VerificationException("invalid webhook signature header");}
        String keyId=parts.get("key"),eventId=parts.get("event"),provided=parts.get("v1");if(keyId==null||eventId==null||provided==null)throw new VerificationException("invalid webhook signature header");if(Math.abs(now.getEpochSecond()-timestamp)>toleranceSeconds)throw new VerificationException("webhook timestamp outside tolerance");String digest=Signing.digest(rawBody);if(!MessageDigest.isEqual(digest.getBytes(StandardCharsets.UTF_8),contentDigest.getBytes(StandardCharsets.UTF_8)))throw new VerificationException("webhook content digest mismatch");String secret=resolver.resolve(keyId).orElseThrow(()->new VerificationException("unknown webhook key"));
        try{Mac mac=Mac.getInstance("HmacSHA256");mac.init(new SecretKeySpec(secret.getBytes(StandardCharsets.UTF_8),"HmacSHA256"));mac.update((eventId+"."+timestamp+".").getBytes(StandardCharsets.UTF_8));String expected=Signing.base64Url(mac.doFinal(rawBody));if(!MessageDigest.isEqual(expected.getBytes(StandardCharsets.US_ASCII),provided.getBytes(StandardCharsets.US_ASCII)))throw new VerificationException("webhook signature mismatch");Models.WebhookEvent event=JSON.readValue(rawBody,Models.WebhookEvent.class);if(!eventId.equals(event.eventId())||!"1".equals(event.schemaVersion()))throw new VerificationException("webhook envelope mismatch");return new VerifiedWebhook(event,eventId,keyId,timestamp,digest);}catch(VerificationException error){throw error;}catch(Exception error){throw new VerificationException("invalid webhook JSON");}
    }
    public static Map<String,String> acknowledgement(String eventId){return Map.of("acknowledged_event_id",eventId);}
}
