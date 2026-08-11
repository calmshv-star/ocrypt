<?php
declare(strict_types=1);

require_once __DIR__ . '/../src/Models.php';
require_once __DIR__ . '/../src/Signing.php';
require_once __DIR__ . '/../src/Webhooks.php';

use MerchantPlatform\Signing;
use MerchantPlatform\Webhooks;

function check(bool $condition, string $message): void
{
    if (!$condition) {
        throw new RuntimeException($message);
    }
}

$fixture = file_get_contents(__DIR__ . '/../../fixtures/golden-vectors.json');
if ($fixture === false) {
    throw new RuntimeException('unable to read golden vectors');
}
$vectors = json_decode($fixture, true, 512, JSON_THROW_ON_ERROR);

check(
    Signing::canonicalQuery($vectors['canonical_query']['input']) === $vectors['canonical_query']['output'],
    'canonical query mismatch'
);

$request = $vectors['request'];
$headers = Signing::request(
    $request['key_id'],
    $request['secret'],
    $request['method'],
    $request['path_and_query'],
    $request['body'],
    $request['timestamp'],
    $request['nonce']
);
check($headers->contentDigest === $request['content_digest'], 'content digest mismatch');
check($headers->signature === $request['signature'], 'request signature mismatch');

$webhook = $vectors['webhook'];
$verified = Webhooks::verify(
    $webhook['body'],
    $webhook['signature_header'],
    $webhook['content_digest'],
    fn(string $key): ?string => $key === $webhook['key_id'] ? $webhook['secret'] : null,
    $webhook['timestamp']
);
check($verified->eventId === $webhook['event_id'], 'webhook event binding mismatch');

try {
    Webhooks::verify(
        $webhook['body'] . ' ',
        $webhook['signature_header'],
        $webhook['content_digest'],
        fn(string $key): ?string => $key === $webhook['key_id'] ? $webhook['secret'] : null,
        $webhook['timestamp']
    );
    throw new RuntimeException('tampered webhook was accepted');
} catch (MerchantPlatform\WebhookVerificationException) {
    // Expected: the digest/signature no longer binds the exact raw body.
}

echo "golden vectors passed\n";
