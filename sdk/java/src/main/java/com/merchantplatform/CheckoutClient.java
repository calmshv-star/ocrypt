package com.merchantplatform;

import com.fasterxml.jackson.core.JsonProcessingException;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.PropertyNamingStrategies;
import com.fasterxml.jackson.databind.JsonNode;
import java.io.IOException;
import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.net.http.HttpTimeoutException;
import java.time.Duration;
import java.util.Set;
import java.util.Map;

public final class CheckoutClient {
    private final String baseUrl;
    private final Duration timeout;
    private final HttpClient http;
    private final ObjectMapper json = new ObjectMapper().setPropertyNamingStrategy(PropertyNamingStrategies.SNAKE_CASE);
    public CheckoutClient(String baseUrl, Duration timeout) {
        MerchantClient.validateBaseUrl(baseUrl); this.baseUrl=baseUrl.replaceAll("/$",""); this.timeout=timeout==null?Duration.ofSeconds(10):timeout;
        this.http=HttpClient.newBuilder().connectTimeout(this.timeout).followRedirects(HttpClient.Redirect.NEVER).build();
    }
    public Models.CheckoutSession getSession(String token) throws MerchantClient.ApiException {
        if (token==null || !token.matches("^cs_[A-Za-z0-9_-]{43}$")) throw new IllegalArgumentException("invalid checkout token");
        try {
            HttpRequest request=HttpRequest.newBuilder(URI.create(baseUrl+"/v1/checkout-sessions/"+Signing.pathSegment(token))).timeout(timeout).header("Accept","application/json").GET().build();
            HttpResponse<byte[]> response=http.send(request,HttpResponse.BodyHandlers.ofByteArray());
            if(response.statusCode()!=200) throw new MerchantClient.ApiException(response.statusCode(),"checkout_unavailable","checkout session unavailable",null,null,response.statusCode()==429||response.statusCode()>=500);
            Models.CheckoutSession value=json.readValue(response.body(),Models.CheckoutSession.class); validate(value); return value;
        } catch (MerchantClient.ApiException error) { throw error; }
        catch (JsonProcessingException error) { throw new MerchantClient.ApiException(200,"invalid_response","invalid checkout response",null,null,false); }
        catch (HttpTimeoutException error) { throw new MerchantClient.ApiException(0,"timeout","checkout request timed out",null,null,true); }
        catch (InterruptedException error) { Thread.currentThread().interrupt(); throw new MerchantClient.ApiException(0,"transport_error","checkout request interrupted",null,null,true); }
        catch (IOException error) { throw new MerchantClient.ApiException(0,"transport_error","checkout request failed",null,null,true); }
    }
    public JsonNode getPaymentLink(String token)throws MerchantClient.ApiException{return publicRequest("GET","/v1/public/payment-links/"+Signing.pathSegment(token(token,"pl")),null,null,null);}
    public JsonNode redeemPaymentLink(String token,String key,Map<String,Object>value,String origin)throws MerchantClient.ApiException{return publicRequest("POST","/v1/public/payment-links/"+Signing.pathSegment(token(token,"pl"))+"/redeem",value==null?Map.of():value,key,origin);}
    public Models.CheckoutSession selectRoute(String token,String routeId,String key,String origin)throws MerchantClient.ApiException{JsonNode node=publicRequest("POST","/v1/checkout-sessions/"+Signing.pathSegment(token(token,"cs"))+"/select-route",Map.of("route_id",routeId),key,origin);try{Models.CheckoutSession value=json.treeToValue(node,Models.CheckoutSession.class);validate(value);return value;}catch(MerchantClient.ApiException error){throw error;}catch(Exception error){throw invalid();}}
    private JsonNode publicRequest(String method,String path,Object payload,String key,String origin)throws MerchantClient.ApiException{try{byte[]body=payload==null?new byte[0]:json.writeValueAsBytes(payload);HttpRequest.Builder builder=HttpRequest.newBuilder(URI.create(baseUrl+path)).timeout(timeout).header("Accept","application/json");if(payload!=null)builder.header("Content-Type","application/json");if(key!=null)builder.header("Idempotency-Key",key);if(origin!=null)builder.header("Origin",origin);builder.method(method,payload==null?HttpRequest.BodyPublishers.noBody():HttpRequest.BodyPublishers.ofByteArray(body));HttpResponse<byte[]>response=http.send(builder.build(),HttpResponse.BodyHandlers.ofByteArray());if(response.statusCode()<200||response.statusCode()>=300)throw new MerchantClient.ApiException(response.statusCode(),"checkout_unavailable","public checkout request failed",null,null,response.statusCode()==429||response.statusCode()>=500);return json.readTree(response.body());}catch(MerchantClient.ApiException error){throw error;}catch(Exception error){throw new MerchantClient.ApiException(0,"transport_error","public checkout request failed",null,null,true);}}
    private static String token(String value,String prefix){if(value==null||!value.matches("^"+prefix+"_[A-Za-z0-9_-]{43}$"))throw new IllegalArgumentException("invalid capability token");return value;}
    private static void validate(Models.CheckoutSession value) throws MerchantClient.ApiException {
        Set<String> statuses=Set.of("pending","detected","confirming","settled","expired","preparing_payment_route","payment_route_failed");
        if(value==null||!statuses.contains(value.status())||value.routes()==null||value.selectedRouteId()==null) throw invalid();
        boolean waiting=value.status().equals("preparing_payment_route")||value.status().equals("payment_route_failed");
        if(waiting?(!value.routes().isEmpty()||!value.selectedRouteId().isEmpty()):value.routes().isEmpty())throw invalid();
        boolean selected=value.selectedRouteId().isEmpty();
        for(Models.CheckoutRoute route:value.routes()){boolean onChain="on_chain".equals(route.provider())&&route.network()!=null&&route.address()!=null&&route.providerId()==null&&route.paymentUrl()==null;boolean hosted="hosted_gateway".equals(route.provider())&&route.providerId()!=null&&route.paymentUrl()!=null&&route.paymentUrl().startsWith("https://")&&route.network()==null&&route.address()==null&&route.transactionHash()==null&&route.explorerUrl()==null;if(route.id()==null||route.asset()==null||route.amount()==null||!route.amount().matches("^\\d+(\\.\\d+)?$")||(!onChain&&!hosted))throw invalid();if(route.id().equals(value.selectedRouteId()))selected=true;}
        if(!selected)throw invalid();
    }
    private static MerchantClient.ApiException invalid(){return new MerchantClient.ApiException(200,"invalid_response","invalid checkout response",null,null,false);}
}
