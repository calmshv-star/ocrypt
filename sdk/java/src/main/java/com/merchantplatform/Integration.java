package com.merchantplatform;

import java.time.Duration;
import java.util.Map;
import java.util.concurrent.ThreadLocalRandom;
import java.util.function.Consumer;

public final class Integration {
    private Integration(){}
    public record EndpointConfig(String environment,String baseUrl){public static EndpointConfig live(String url){return new EndpointConfig("live",url);}public static EndpointConfig sandbox(String url){return new EndpointConfig("sandbox",url);}}
    public record RetryPolicy(int maxAttempts,Duration baseDelay,Duration maxDelay,double jitterRatio){public RetryPolicy(){this(4,Duration.ofMillis(200),Duration.ofSeconds(5),0.2);}public RetryPolicy{if(maxAttempts<1||maxAttempts>10)throw new IllegalArgumentException("maxAttempts must be 1..10");}}
    @FunctionalInterface public interface Checked<T>{T run()throws MerchantClient.ApiException;}
    @FunctionalInterface public interface TelemetryHook{void accept(Map<String,Object>event);}
    public static <T>T withRetry(Checked<T>action,boolean safe,String idempotencyKey,RetryPolicy policy)throws MerchantClient.ApiException,InterruptedException{if(!safe&&(idempotencyKey==null||idempotencyKey.isEmpty()))throw new IllegalArgumentException("unsafe retries require an idempotency key");for(int attempt=1;;attempt++){try{return action.run();}catch(MerchantClient.ApiException error){if(!error.retryable||attempt>=policy.maxAttempts())throw error;long exponential=Math.min(policy.maxDelay().toMillis(),policy.baseDelay().toMillis()*(1L<<(attempt-1))),delay;if(error.retryAfterSeconds>0){delay=Math.min(policy.maxDelay().toMillis(),error.retryAfterSeconds*1000);}else{delay=(long)(exponential*(1+(ThreadLocalRandom.current().nextDouble()*2-1)*policy.jitterRatio()));}Thread.sleep(Math.max(0,delay));}}}
    public static <T>T instrument(String operation,String method,TelemetryHook hook,Checked<T>action)throws MerchantClient.ApiException{if(!operation.matches("^[a-z][a-z0-9_.-]{0,63}$")||!method.matches("^[A-Z]{3,7}$"))throw new IllegalArgumentException("telemetry operation or method is not low-cardinality");long started=System.nanoTime();if(hook!=null)hook.accept(Map.of("phase","start","operation",operation,"method",method));try{T value=action.run();if(hook!=null)hook.accept(Map.of("phase","end","operation",operation,"method",method,"status",200,"duration_ms",(System.nanoTime()-started)/1_000_000));return value;}catch(MerchantClient.ApiException error){if(hook!=null)hook.accept(Map.of("phase","end","operation",operation,"method",method,"status",error.status,"duration_ms",(System.nanoTime()-started)/1_000_000,"retryable",error.retryable));throw error;}}
    public static void paymentIntents(MerchantClient client,String status,int pageSize,Consumer<Models.PaymentIntent>consumer)throws MerchantClient.ApiException{String after=null;for(;;){var page=client.listPaymentIntents(status,after,pageSize).data();page.items().forEach(consumer);if(page.nextCursor()==null||page.nextCursor().isEmpty()||page.nextCursor().equals(after))return;after=page.nextCursor();}}
    public static String events(MerchantClient client,long afterSequence,int pageSize,Consumer<Models.PublicEvent>consumer)throws MerchantClient.ApiException{long cursor=afterSequence;for(;;){var page=client.listEvents(cursor,pageSize).data();page.items().forEach(consumer);if(page.items().isEmpty()||Long.toString(cursor).equals(page.nextSequence()))return page.nextSequence();cursor=Long.parseLong(page.nextSequence());}}
}
