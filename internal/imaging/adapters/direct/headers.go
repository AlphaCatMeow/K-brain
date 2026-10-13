package direct

import (
	"net/http"
	"strings"
)

// customHeaders applies an explicit namespace whitelist, then reserves names
// that could change auth, request routing/framing, method or replay semantics.
// Non-X names must be individually listed; caller supplied auth never wins.
func customHeaders(in map[string]string) (http.Header, error) {
	out := make(http.Header)
	if len(in) > 32 {
		return nil, badInput()
	}
	total := 0
	for name, value := range in {
		if len(name) == 0 || len(name) > 128 || len(value) > 4096 {
			return nil, badInput()
		}
		for _, c := range name {
			if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return nil, badInput()
			}
		}
		lower := strings.ToLower(name)
		allowed := lower == "openai-organization" || lower == "openai-project" || strings.HasPrefix(lower, "x-") && len(lower) > 2
		if !allowed {
			return nil, badInput()
		}
		for _, reserved := range []string{"auth", "key", "token", "secret", "credential", "cookie", "idempotency", "forward", "proxy", "host", "content-", "encoding", "connection", "transfer", "upgrade", "method", "original-url", "rewrite-url"} {
			if strings.Contains(lower, reserved) {
				return nil, badInput()
			}
		}
		for _, c := range value {
			if c < 0x20 || c == 0x7f {
				return nil, badInput()
			}
		}
		key := http.CanonicalHeaderKey(name)
		if _, exists := out[key]; exists {
			return nil, badInput()
		}
		total += len(name) + len(value)
		if total > 16<<10 {
			return nil, badInput()
		}
		out.Set(key, value)
	}
	return out, nil
}
