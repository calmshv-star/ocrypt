package merchantplatform

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

type SignedHeaders struct{ KeyID, Timestamp, Nonce, ContentDigest, Signature string }

func (h SignedHeaders) Apply(target map[string]string) {
	target["Merchant-Key-Id"] = h.KeyID
	target["Merchant-Timestamp"] = h.Timestamp
	target["Merchant-Nonce"] = h.Nonce
	target["Content-Digest"] = h.ContentDigest
	target["Merchant-Signature"] = h.Signature
}
func SignRequest(keyID string, secret []byte, method, pathAndQuery string, body []byte, timestamp int64, nonce string) SignedHeaders {
	digest := sha256.Sum256(body)
	stamp := strconv.FormatInt(timestamp, 10)
	canonical := strings.Join([]string{strings.ToUpper(method), pathAndQuery, stamp, nonce, hex.EncodeToString(digest[:])}, "\n")
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(canonical))
	return SignedHeaders{keyID, stamp, nonce, "sha-256=:" + base64.StdEncoding.EncodeToString(digest[:]) + ":", base64.RawURLEncoding.EncodeToString(mac.Sum(nil))}
}
func RandomNonce() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	return hex.EncodeToString(value), nil
}
func CanonicalQuery(values url.Values) string { // net/url matches the server: sorted keys, stable repeated-value order and '+' spaces.
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var pairs []string
	for _, key := range keys {
		for _, value := range values[key] {
			pairs = append(pairs, url.QueryEscape(key)+"="+url.QueryEscape(value))
		}
	}
	return strings.Join(pairs, "&")
}
