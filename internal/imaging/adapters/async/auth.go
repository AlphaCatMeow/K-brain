package asyncadapter

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"sort"
	"strings"
	"time"
)

func hash(raw []byte) string { s := sha256.Sum256(raw); return hex.EncodeToString(s[:]) }
func mac(key []byte, text string) []byte {
	h := hmac.New(sha256.New, key)
	_, _ = h.Write([]byte(text))
	return h.Sum(nil)
}

// signVisual follows research J2/J3/J4. The returned canonical request is used
// only by offline tests; callers must never log it or the Authorization header.
func signVisual(req *http.Request, body []byte, ak, sk, token string, now time.Time) string {
	date := now.UTC().Format("20060102T150405Z")
	day := now.UTC().Format("20060102")
	payload := hash(body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Date", date)
	req.Header.Set("X-Content-Sha256", payload)
	headers := map[string]string{"content-type": "application/json", "host": req.URL.Host, "x-content-sha256": payload, "x-date": date}
	if token != "" {
		req.Header.Set("X-Security-Token", token)
		headers["x-security-token"] = token
	}
	names := make([]string, 0, len(headers))
	for k := range headers {
		names = append(names, k)
	}
	sort.Strings(names)
	var canonicalHeaders strings.Builder
	for _, k := range names {
		canonicalHeaders.WriteString(k + ":" + strings.TrimSpace(headers[k]) + "\n")
	}
	signed := strings.Join(names, ";")
	// Action and Version are fixed ASCII values. Query.Encode sorts their names;
	// replace '+' for RFC3986 spaces as specified by the official algorithm.
	query := strings.ReplaceAll(req.URL.Query().Encode(), "+", "%20")
	req.URL.RawQuery = query
	canonical := req.Method + "\n/\n" + query + "\n" + canonicalHeaders.String() + "\n" + signed + "\n" + payload
	scope := day + "/cn-north-1/cv/request"
	toSign := "HMAC-SHA256\n" + date + "\n" + scope + "\n" + hash([]byte(canonical))
	key := mac([]byte(sk), day)
	key = mac(key, "cn-north-1")
	key = mac(key, "cv")
	key = mac(key, "request")
	req.Header.Set("Authorization", "HMAC-SHA256 Credential="+ak+"/"+scope+", SignedHeaders="+signed+", Signature="+hex.EncodeToString(mac(key, toSign)))
	return canonical
}
