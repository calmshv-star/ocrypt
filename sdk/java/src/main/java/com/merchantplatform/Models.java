package com.merchantplatform;

import java.util.List;
import java.util.Map;

public final class Models {
    private Models() {}
    public record RouteSelector(String provider,String chainId,String providerId,String assetId) {
        public static RouteSelector onChain(String chainId,String assetId){return new RouteSelector("on_chain",chainId,null,assetId);}
        public static RouteSelector hostedGateway(String providerId,String assetId){return new RouteSelector("hosted_gateway",null,providerId,assetId);}
    }
    public record CreatePaymentIntentRequest(String merchantOrderId, String amountMinor, String currency, int currencyScale, String description, String customerReference, Integer expiresIn, String expiresAt, List<RouteSelector> allowedRoutes, Map<String,Object> metadata) {
        public CreatePaymentIntentRequest { requireAmount(amountMinor, true); }
        public CreatePaymentIntentRequest(String order, String amount, String currency, int scale) { this(order, amount, currency, scale, null, null, null, null, null, null); }
    }
    public record OnChainRouteRequest(String chainId,String assetId) {}
    public record HostedGatewayRouteRequest(String providerId,String assetId) {}
    public record CreatePaymentRouteRequest(String provider,OnChainRouteRequest onChain,HostedGatewayRouteRequest hostedGateway,Integer expiresIn) {
        public static CreatePaymentRouteRequest onChain(String chainId,String assetId,Integer expiresIn){return new CreatePaymentRouteRequest("on_chain",new OnChainRouteRequest(chainId,assetId),null,expiresIn);}
        public static CreatePaymentRouteRequest hostedGateway(String providerId,String assetId,Integer expiresIn){return new CreatePaymentRouteRequest("hosted_gateway",null,new HostedGatewayRouteRequest(providerId,assetId),expiresIn);}
    }
    public record CancelPaymentIntentRequest(String reason, Long expectedVersion) {}
    public record ExpirePaymentIntentRequest(String reason, long expectedVersion) {}
    public record UpdatePaymentIntentMetadataRequest(long expectedVersion, Map<String,Object> metadata) {}
    public record CreateReconciliationReportRequest(String periodStart,String periodEnd,String format) { public CreateReconciliationReportRequest(String start,String end){this(start,end,"jsonl_v1");} }
    public record SubmitPaymentProofRequest(String paymentIntentId, String chainId, String transactionId) {}
    public record PaymentRoute(String id,String intentId,String chainId,String assetId,String provider,String providerId,String providerOrderId,String providerReference,String paymentUrl,String expectedAmountAtomic,int assetDecimals,String displayAmount,String address,String memo,long requiredFinality,String status,long version,String startsAt,String expiresAt,String graceEndsAt) {}
    public record PaymentIntent(String id, String merchantId, String merchantOrderId, String customerReference, String amountMinor, String currency, int currencyScale, String description, String status, String statusReason, Map<String,Object> metadata, List<RouteSelector> allowedRoutes, long version, String createdAt, String updatedAt, String expiresAt, String settledAt, String cancelledAt, List<PaymentRoute> routes, String checkoutToken) {}
    public record PaymentProof(String id, String merchantId, String paymentIntentId, String chainId, String transactionId, String status, List<String> transferEventIds, String createdAt, String updatedAt, long version) {}
    public record Asset(String id, String chainId, String symbol, String name, String kind, String contract, int decimals, String status, String minimumDepositAtomic) {}
    public record WebhookEvent(String eventId, String eventType, String schemaVersion, long sequence, String occurredAt, String merchantId, boolean livemode, Map<String,Object> paymentIntent, Map<String,Object> settlement, Map<String,Object> observation, Map<String,Object> resolution) {}
    public record Envelope<T>(T data, String requestId, String apiVersion) {}
    public record CursorPage<T>(List<T> items, String nextCursor) {}
    public record EventPage<T>(List<T> items,String nextCursor,String nextSequence) {}
    public record PublicEvent(String eventId,String eventType,String schemaVersion,String aggregateId,String aggregateType,long aggregateVersion,long sequence,Object payload,String occurredAt) {}
    public record ReconciliationReport(String id,String status,String format,String periodStart,String periodEnd,String snapshotLedgerSequence,String snapshotCutoff,int attemptCount,String lastErrorCode,String objectSizeBytes,String objectSha256,String signature,String signingKeyId,String downloadPath,String createdAt,String updatedAt,String completedAt,long version) {}
    public record ReportDownload(byte[] bytes,String sha256,String signature,String signingKeyId) {}
    public record CheckoutRoute(String id,String provider,String providerId,String network,String asset,String amount,String address,String paymentUrl,String transactionHash,String explorerUrl) {}
    public record CheckoutSession(String intentId, String orderId, String status, String expiresAt, String selectedRouteId, List<CheckoutRoute> routes) {}
    public static void requireAmount(String value, boolean positive) { if (value == null || !value.matches(positive ? "^[1-9][0-9]{0,77}$" : "^(0|[1-9][0-9]{0,77})$")) throw new IllegalArgumentException("amount must be a canonical integer string"); }
}
