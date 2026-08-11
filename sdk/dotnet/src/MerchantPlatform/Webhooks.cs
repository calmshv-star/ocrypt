using System.Security.Cryptography;
using System.Text;
using System.Text.Json;

namespace MerchantPlatform;

public enum InboxResult { Processed, Duplicate, Conflict }
public interface IWebhookInbox<TTransaction> { Task<InboxResult> ProcessAsync(string eventId,string bodyDigest,Func<TTransaction,Task> handler,CancellationToken cancellationToken=default); }
public sealed record VerifiedWebhook(WebhookEvent Event,string EventId,string KeyId,long Timestamp,string BodyDigest);
public sealed class WebhookVerificationException(string message):Exception(message);
public static class Webhooks {
    private static readonly JsonSerializerOptions Json=new(){PropertyNamingPolicy=JsonNamingPolicy.SnakeCaseLower,PropertyNameCaseInsensitive=true};
    public static VerifiedWebhook Verify(ReadOnlySpan<byte> rawBody,string signatureHeader,string contentDigest,Func<string,string?> resolveSecret,DateTimeOffset? now=null,long toleranceSeconds=300){var parts=new Dictionary<string,string>();foreach(var item in signatureHeader.Split(',')){var pair=item.Trim().Split('=',2);if(pair.Length==2)parts[pair[0]]=pair[1];}if(!parts.TryGetValue("t",out var stamp)||!long.TryParse(stamp,out var timestamp)||!parts.TryGetValue("key",out var keyId)||!parts.TryGetValue("event",out var eventId)||!parts.TryGetValue("v1",out var provided))throw new WebhookVerificationException("invalid webhook signature header");if(Math.Abs((now??DateTimeOffset.UtcNow).ToUnixTimeSeconds()-timestamp)>toleranceSeconds)throw new WebhookVerificationException("webhook timestamp outside tolerance");var digest=Signing.Digest(rawBody);if(!Fixed(digest,contentDigest))throw new WebhookVerificationException("webhook content digest mismatch");var secret=resolveSecret(keyId);if(string.IsNullOrEmpty(secret))throw new WebhookVerificationException("unknown webhook key");using var hmac=new HMACSHA256(Encoding.UTF8.GetBytes(secret));var prefix=Encoding.UTF8.GetBytes($"{eventId}.{timestamp}.");var input=new byte[prefix.Length+rawBody.Length];prefix.CopyTo(input,0);rawBody.CopyTo(input.AsSpan(prefix.Length));var expected=Signing.Base64Url(hmac.ComputeHash(input));if(!Fixed(expected,provided))throw new WebhookVerificationException("webhook signature mismatch");WebhookEvent? value;try{value=JsonSerializer.Deserialize<WebhookEvent>(rawBody,Json);}catch(JsonException){throw new WebhookVerificationException("invalid webhook JSON");}if(value is null||value.EventId!=eventId||value.SchemaVersion!="1")throw new WebhookVerificationException("webhook envelope mismatch");return new VerifiedWebhook(value,eventId,keyId,timestamp,digest);}
    public static IReadOnlyDictionary<string,string> Acknowledgement(string eventId)=>new Dictionary<string,string>{{"acknowledged_event_id",eventId}};
    private static bool Fixed(string left,string right){var a=Encoding.UTF8.GetBytes(left);var b=Encoding.UTF8.GetBytes(right);return a.Length==b.Length&&CryptographicOperations.FixedTimeEquals(a,b);}
}
