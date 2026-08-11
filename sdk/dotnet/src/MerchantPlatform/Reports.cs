using System.Security.Cryptography;
using System.Text;

namespace MerchantPlatform;

public static class Reports {
    public static byte[] SignatureMessage(string reportId,string snapshotSequence,ReadOnlySpan<byte> digest){if(digest.Length!=32)throw new ArgumentException("SHA-256 digest must be 32 bytes");var prefix=Encoding.UTF8.GetBytes($"merchant-reconciliation-jsonl-v1\0{reportId}\0{snapshotSequence}\0");var result=new byte[prefix.Length+digest.Length];prefix.CopyTo(result,0);digest.CopyTo(result.AsSpan(prefix.Length));return result;}
    public static byte[] ValidateDigest(ReadOnlySpan<byte> raw,ReconciliationReport report){var digest=SHA256.HashData(raw);if(report.Status!="ready"||!CryptographicOperations.FixedTimeEquals(Encoding.ASCII.GetBytes(Convert.ToHexString(digest).ToLowerInvariant()),Encoding.ASCII.GetBytes(report.ObjectSha256??"")))throw new CryptographicException("reconciliation report digest mismatch");if(report.ObjectSizeBytes is not null&&long.Parse(report.ObjectSizeBytes)!=raw.Length)throw new CryptographicException("reconciliation report size mismatch");return digest;}
}
