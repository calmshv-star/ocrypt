package com.merchantplatform;

import javax.crypto.Mac;
import javax.crypto.spec.SecretKeySpec;
import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.SecureRandom;
import java.util.ArrayList;
import java.util.Base64;
import java.util.Comparator;
import java.util.List;
import java.util.Map;

public final class Signing {
    private Signing() {}
    public record SignedHeaders(String keyId, String timestamp, String nonce, String contentDigest, String signature) {
        public Map<String,String> asMap() { return Map.of("Merchant-Key-Id",keyId,"Merchant-Timestamp",timestamp,"Merchant-Nonce",nonce,"Content-Digest",contentDigest,"Merchant-Signature",signature); }
    }
    public static SignedHeaders signRequest(String keyId, String secret, String method, String pathAndQuery, byte[] body, long timestamp, String nonce) {
        try {
            byte[] digest=MessageDigest.getInstance("SHA-256").digest(body); String hex=hex(digest); String canonical=method.toUpperCase()+"\n"+pathAndQuery+"\n"+timestamp+"\n"+nonce+"\n"+hex;
            Mac mac=Mac.getInstance("HmacSHA256");mac.init(new SecretKeySpec(secret.getBytes(StandardCharsets.UTF_8),"HmacSHA256"));String signature=Base64.getUrlEncoder().withoutPadding().encodeToString(mac.doFinal(canonical.getBytes(StandardCharsets.UTF_8)));
            return new SignedHeaders(keyId,Long.toString(timestamp),nonce,"sha-256=:"+Base64.getEncoder().encodeToString(digest)+":",signature);
        } catch (Exception error) { throw new IllegalStateException("request signing unavailable", error); }
    }
    public static String canonicalQuery(Map<String,? extends List<?>> query) { List<String> pairs=new ArrayList<>();query.entrySet().stream().sorted(Comparator.comparing(Map.Entry::getKey)).forEach(entry->{for(Object value:entry.getValue())if(value!=null)pairs.add(formEncode(entry.getKey())+"="+formEncode(String.valueOf(value)));});return String.join("&",pairs); }
    public static String pathSegment(String value) { return percentEncode(value, false); }
    public static String randomNonce() { byte[] bytes=new byte[16];new SecureRandom().nextBytes(bytes);return hex(bytes); }
    public static String base64Url(byte[] value) { return Base64.getUrlEncoder().withoutPadding().encodeToString(value); }
    public static String digest(byte[] value) { try{return "sha-256=:"+Base64.getEncoder().encodeToString(MessageDigest.getInstance("SHA-256").digest(value))+":";}catch(Exception error){throw new IllegalStateException(error);} }
    private static String formEncode(String value) { return percentEncode(value, true); }
    private static String percentEncode(String value, boolean plusSpace) { StringBuilder result=new StringBuilder();for(byte item:value.getBytes(StandardCharsets.UTF_8)){int b=item&255;if((b>='a'&&b<='z')||(b>='A'&&b<='Z')||(b>='0'&&b<='9')||b=='-'||b=='.'||b=='_'||b=='~')result.append((char)b);else if(plusSpace&&b==' ')result.append('+');else result.append('%').append(String.format("%02X",b));}return result.toString(); }
    private static String hex(byte[] value) { StringBuilder result=new StringBuilder();for(byte item:value)result.append(String.format("%02x",item));return result.toString(); }
}
