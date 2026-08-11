using System.Text;
using System.Text.Json;
using MerchantPlatform;

var fixture = Path.Combine(AppContext.BaseDirectory, "golden-vectors.json");
var vectors = JsonDocument.Parse(File.ReadAllText(fixture)).RootElement;
var query = vectors.GetProperty("canonical_query");
var queryInput = query.GetProperty("input").EnumerateObject().ToDictionary(
    item => item.Name,
    item => (IReadOnlyList<object?>)item.Value.EnumerateArray().Select(value => (object?)value.GetString()).ToArray());
if (Signing.CanonicalQuery(queryInput) != query.GetProperty("output").GetString()) throw new Exception("canonical query mismatch");

var request = vectors.GetProperty("request");
var headers = Signing.SignRequest(
    request.GetProperty("key_id").GetString()!, request.GetProperty("secret").GetString()!,
    request.GetProperty("method").GetString()!, request.GetProperty("path_and_query").GetString()!,
    Encoding.UTF8.GetBytes(request.GetProperty("body").GetString()!), request.GetProperty("timestamp").GetInt64(),
    request.GetProperty("nonce").GetString()!);
if (headers.ContentDigest != request.GetProperty("content_digest").GetString() || headers.Signature != request.GetProperty("signature").GetString()) throw new Exception("request vector mismatch");

var webhook = vectors.GetProperty("webhook");
var verified = Webhooks.Verify(
    Encoding.UTF8.GetBytes(webhook.GetProperty("body").GetString()!), webhook.GetProperty("signature_header").GetString()!,
    webhook.GetProperty("content_digest").GetString()!,
    key => key == webhook.GetProperty("key_id").GetString() ? webhook.GetProperty("secret").GetString() : null,
    DateTimeOffset.FromUnixTimeSeconds(webhook.GetProperty("timestamp").GetInt64()));
if (verified.EventId != webhook.GetProperty("event_id").GetString()) throw new Exception("webhook vector mismatch");
Console.WriteLine("golden vectors passed");
