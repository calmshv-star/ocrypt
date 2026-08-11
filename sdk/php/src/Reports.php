<?php
declare(strict_types=1);
namespace MerchantPlatform;

final class Reports {
    public static function verify(string $raw,array $report,array $publicKeys): void{$digest=hash('sha256',$raw,true);if(($report['status']??'')!=='ready'||!hash_equals((string)($report['object_sha256']??''),bin2hex($digest)))throw new \RuntimeException('reconciliation report digest mismatch');if(isset($report['object_size_bytes'])&&(int)$report['object_size_bytes']!==strlen($raw))throw new \RuntimeException('reconciliation report size mismatch');$keyId=(string)($report['signing_key_id']??'');if(!isset($publicKeys[$keyId]))throw new \RuntimeException('unknown reconciliation signing key: '.$keyId);$signature=self::base64UrlDecode((string)$report['signature']);$message="merchant-reconciliation-jsonl-v1\0".$report['id']."\0".$report['snapshot_ledger_sequence']."\0".$digest;if(!function_exists('sodium_crypto_sign_verify_detached'))throw new \RuntimeException('ext-sodium is required for report verification');if(!sodium_crypto_sign_verify_detached($signature,$message,$publicKeys[$keyId]))throw new \RuntimeException('reconciliation report signature mismatch');}
    private static function base64UrlDecode(string $value): string{$decoded=base64_decode(strtr($value,'-_','+/').str_repeat('=',(4-strlen($value)%4)%4),true);if($decoded===false)throw new \RuntimeException('invalid report signature');return $decoded;}
}
