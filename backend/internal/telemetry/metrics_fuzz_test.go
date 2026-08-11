package telemetry

import (
	"strings"
	"testing"
)

func FuzzCanonicalRouteNeverReturnsRawParameters(fuzzer *testing.F) {
	fuzzer.Add("GET /v1/payment-intents/{customer_secret}")
	fuzzer.Add("https://user:password@example.invalid/path?token=secret")
	fuzzer.Add("/v1/transfers/{tx}")
	fuzzer.Fuzz(func(t *testing.T, pattern string) {
		route := canonicalRoute(pattern)
		if len(route) > 160 || strings.ContainsAny(route, "?&#@\n\r\t\"") {
			t.Fatalf("unsafe canonical route %q from %q", route, pattern)
		}
		for remainder := pattern; ; {
			start := strings.IndexByte(remainder, '{')
			end := strings.IndexByte(remainder, '}')
			if start < 0 || end <= start {
				break
			}
			name := remainder[start+1 : end]
			if name != "" && name != "param" && route != "_other" && strings.Contains(route, "{"+name+"}") {
				t.Fatalf("parameter name %q leaked into route %q", name, route)
			}
			remainder = remainder[end+1:]
		}
	})
}
