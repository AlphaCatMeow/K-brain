package backend

import (
	"net/url"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

func publicHeaders(headers []config.CustomHeader) []config.CustomHeader {
	out := make([]config.CustomHeader, 0, len(headers))
	for _, h := range headers {
		v := h.Value
		if secretField(h.Key) {
			v = ""
		}
		out = append(out, config.CustomHeader{Key: h.Key, Value: v})
	}
	return out
}
func publicModelsURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return ""
	}
	u.User = nil
	q := u.Query()
	for k := range q {
		if secretField(k) {
			q.Del(k)
		}
	}
	u.RawQuery = q.Encode()
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/")
}
