using System.Security.Cryptography;
using System.Text;

namespace MerchantPlatform;

public sealed record SignedHeaders(string KeyId, string Timestamp, string Nonce, string ContentDigest, string Signature) {
    public IReadOnlyDictionary<string,string> AsDictionary() => new Dictionary<string,string>{{"Merchant-Key-Id",KeyId},{"Merchant-Timestamp",Timestamp},{"Merchant-Nonce",Nonce},{"Content-Digest",ContentDigest},{"Merchant-Signature",Signature}};
}
public static class Signing {
    public static SignedHeaders SignRequest(string keyId,string secret,string method,string pathAndQuery,ReadOnlySpan<byte> body,long timestamp,string nonce){var digest=SHA256.HashData(body);var canonical=string.Join("\n",method.ToUpperInvariant(),pathAndQuery,timestamp.ToString(System.Globalization.CultureInfo.InvariantCulture),nonce,Convert.ToHexString(digest).ToLowerInvariant());using var hmac=new HMACSHA256(Encoding.UTF8.GetBytes(secret));var signature=Base64Url(hmac.ComputeHash(Encoding.UTF8.GetBytes(canonical)));return new SignedHeaders(keyId,timestamp.ToString(System.Globalization.CultureInfo.InvariantCulture),nonce,$"sha-256=:{Convert.ToBase64String(digest)}:",signature);}
    public static string CanonicalQuery(IReadOnlyDictionary<string,IReadOnlyList<object?>> query){var pairs=new List<string>();foreach(var item in query.OrderBy(item=>item.Key,StringComparer.Ordinal))foreach(var value in item.Value)if(value is not null)pairs.Add(FormEncode(item.Key)+"="+FormEncode(Convert.ToString(value,System.Globalization.CultureInfo.InvariantCulture)!));return string.Join('&',pairs);}
    public static string RandomNonce()=>Convert.ToHexString(RandomNumberGenerator.GetBytes(16)).ToLowerInvariant();
    public static string PathSegment(string value)=>PercentEncode(value,false);
    public static string Digest(ReadOnlySpan<byte> value)=>$"sha-256=:{Convert.ToBase64String(SHA256.HashData(value))}:";
    public static string Base64Url(byte[] value)=>Convert.ToBase64String(value).TrimEnd('=').Replace('+','-').Replace('/','_');
    private static string FormEncode(string value)=>PercentEncode(value,true);
    private static string PercentEncode(string value,bool plusSpace){var result=new StringBuilder();foreach(var item in Encoding.UTF8.GetBytes(value)){var unreserved=(item>=(byte)'a'&&item<=(byte)'z')||(item>=(byte)'A'&&item<=(byte)'Z')||(item>=(byte)'0'&&item<=(byte)'9')||item==(byte)'-'||item==(byte)'.'||item==(byte)'_'||item==(byte)'~';if(unreserved)result.Append((char)item);else if(plusSpace&&item==(byte)' ')result.Append('+');else result.Append('%').Append(item.ToString("X2"));}return result.ToString();}
}
