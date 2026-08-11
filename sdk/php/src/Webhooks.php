<?php
declare(strict_types=1);

namespace MerchantPlatform;

interface WebhookInbox { public function process(string $eventId, string $bodyDigest, callable $handler): string; }
final class VerifiedWebhook { public function __construct(public readonly WebhookEvent $event, public readonly string $eventId, public readonly string $keyId, public readonly int $timestamp, public readonly string $bodyDigest) {} }
final class WebhookVerificationException extends \RuntimeException {}
final class Webhooks {
    public static function verify(string $rawBody, string $signatureHeader, string $contentDigest, callable $resolveSecret, ?int $now = null, int $toleranceSeconds = 300): VerifiedWebhook {
        $parts = []; foreach (explode(',', $signatureHeader) as $part) { [$key,$value] = array_pad(explode('=', trim($part), 2), 2, null); if ($value !== null) $parts[$key] = $value; }
        if (!isset($parts['t'],$parts['key'],$parts['event'],$parts['v1']) || !ctype_digit($parts['t'])) throw new WebhookVerificationException('invalid webhook signature header');
        $timestamp = (int)$parts['t']; if (abs(($now ?? time()) - $timestamp) > $toleranceSeconds) throw new WebhookVerificationException('webhook timestamp outside tolerance');
        $digest = 'sha-256=:'.base64_encode(hash('sha256', $rawBody, true)).':'; if (!hash_equals($digest, $contentDigest)) throw new WebhookVerificationException('webhook content digest mismatch');
        $secret = $resolveSecret($parts['key']); if (!is_string($secret) || $secret === '') throw new WebhookVerificationException('unknown webhook key');
        $expected = Signing::base64Url(hash_hmac('sha256', $parts['event'].'.'.$timestamp.'.'.$rawBody, $secret, true)); if (!hash_equals($expected, $parts['v1'])) throw new WebhookVerificationException('webhook signature mismatch');
        try { $data = json_decode($rawBody, true, 512, JSON_THROW_ON_ERROR); } catch (\JsonException) { throw new WebhookVerificationException('invalid webhook JSON'); }
        if (($data['event_id'] ?? null) !== $parts['event'] || ($data['schema_version'] ?? null) !== '1') throw new WebhookVerificationException('webhook envelope mismatch');
        return new VerifiedWebhook(WebhookEvent::fromArray($data), $parts['event'], $parts['key'], $timestamp, $digest);
    }
    public static function acknowledgement(string $eventId): array { return ['acknowledged_event_id'=>$eventId]; }
}
