package com.merchantplatform;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Instant;
import java.util.Optional;
import java.util.List;
import java.util.Map;
import static org.junit.jupiter.api.Assertions.*;

final class GoldenVectorTest {
    private final JsonNode vectors;
    GoldenVectorTest() throws Exception { vectors=new ObjectMapper().readTree(Files.readString(Path.of("../fixtures/golden-vectors.json"))); }
    @Test void canonicalQueryMatches() { JsonNode value=vectors.path("canonical_query");Map<String,List<String>> input=new ObjectMapper().convertValue(value.path("input"),new com.fasterxml.jackson.core.type.TypeReference<Map<String,List<String>>>(){});assertEquals(value.path("output").asText(),Signing.canonicalQuery(input)); }
    @Test void requestSigningMatches() { JsonNode v=vectors.path("request");Signing.SignedHeaders headers=Signing.signRequest(v.path("key_id").asText(),v.path("secret").asText(),v.path("method").asText(),v.path("path_and_query").asText(),v.path("body").asText().getBytes(StandardCharsets.UTF_8),v.path("timestamp").asLong(),v.path("nonce").asText());assertEquals(v.path("content_digest").asText(),headers.contentDigest());assertEquals(v.path("signature").asText(),headers.signature()); }
    @Test void webhookVerificationMatches() throws Exception { JsonNode v=vectors.path("webhook");Webhooks.VerifiedWebhook verified=Webhooks.verify(v.path("body").asText().getBytes(StandardCharsets.UTF_8),v.path("signature_header").asText(),v.path("content_digest").asText(),key->key.equals(v.path("key_id").asText())?Optional.of(v.path("secret").asText()):Optional.empty(),Instant.ofEpochSecond(v.path("timestamp").asLong()),300);assertEquals(v.path("event_id").asText(),verified.eventId()); }
}
