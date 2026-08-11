<?php
declare(strict_types=1);

namespace MerchantPlatform;

final class SignedHeaders {
    public function __construct(public readonly string $keyId, public readonly string $timestamp, public readonly string $nonce, public readonly string $contentDigest, public readonly string $signature) {}
    public function toArray(): array { return ['Merchant-Key-Id'=>$this->keyId,'Merchant-Timestamp'=>$this->timestamp,'Merchant-Nonce'=>$this->nonce,'Content-Digest'=>$this->contentDigest,'Merchant-Signature'=>$this->signature]; }
}
final class Signing {
    public static function request(string $keyId, string $secret, string $method, string $pathAndQuery, string $body, int $timestamp, string $nonce): SignedHeaders {
        $digest = hash('sha256', $body, true); $canonical = strtoupper($method)."\n".$pathAndQuery."\n".$timestamp."\n".$nonce."\n".bin2hex($digest);
        $signature = self::base64Url(hash_hmac('sha256', $canonical, $secret, true));
        return new SignedHeaders($keyId, (string)$timestamp, $nonce, 'sha-256=:'.base64_encode($digest).':', $signature);
    }
    public static function canonicalQuery(array $query): string {
        ksort($query, SORT_STRING); $pairs = [];
        foreach ($query as $key => $value) { if ($value === null) continue; foreach (is_array($value) ? $value : [$value] as $item) $pairs[] = self::encode((string)$key).'='.self::encode((string)$item); }
        return implode('&', $pairs);
    }
    public static function base64Url(string $value): string { return rtrim(strtr(base64_encode($value), '+/', '-_'), '='); }
    private static function encode(string $value): string { return str_replace('%20', '+', rawurlencode($value)); }
}
