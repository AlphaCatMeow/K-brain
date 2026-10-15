package backend

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

const ImageDelegationHeader = "X-Kbrain-Image-Delegation"
const imageDelegationDomain = "kbrain.image.delegation.v1."

// ImageDelegationKey is server-owned trust configuration. AllowConfigure is an
// additional ceiling on the signed configuration grant.
type ImageDelegationKey struct {
	PublicKey      ed25519.PublicKey
	AllowConfigure bool
}

type ImageDelegationConfig struct {
	BackendID string
	Keys      map[string]ImageDelegationKey
	// Now is optional and primarily useful for deterministic tests.
	Now func() time.Time
	// MaxNonces bounds replay-cache memory. Saturation denies new requests;
	// unexpired entries are never evicted to admit a replay.
	MaxNonces int
}

type imageDelegationClaims struct {
	Subject      string `json:"sub"`
	BackendID    string `json:"backendId"`
	IssuedAt     int64  `json:"iat"`
	ExpiresAt    int64  `json:"exp"`
	Audience     string `json:"aud"`
	Nonce        string `json:"nonce"`
	CanConfigure bool   `json:"canConfigure"`
}

// NewImageDelegationResolver returns ImageModule.ResolveScope. The module calls
// it only after authenticating the backend bearer. No owner/backend/header
// fallback exists. Trust keys are copied and backend identity is pinned here.
//
// Replay state belongs to one process: deployments must use one verifier per
// backend, or a shared atomic nonce store before load balancing. On restart,
// rotate trust keys (or wait >60 seconds before accepting the old keys).
// This does not make the local-only settings manager multi-tenant.
func NewImageDelegationResolver(cfg ImageDelegationConfig) (ImageScopeResolver, error) {
	if strings.TrimSpace(cfg.BackendID) == "" || len(cfg.Keys) == 0 {
		return nil, errors.New("image delegation trust is not configured")
	}
	keys := make(map[string]ImageDelegationKey, len(cfg.Keys))
	for id, key := range cfg.Keys {
		if id == "" || strings.ContainsAny(id, ".\r\n") || len(key.PublicKey) != ed25519.PublicKeySize {
			return nil, errors.New("invalid image delegation trust key")
		}
		key.PublicKey = append(ed25519.PublicKey(nil), key.PublicKey...)
		keys[id] = key
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	capacity := cfg.MaxNonces
	if capacity <= 0 {
		capacity = 10000
	}
	nonces := make(map[string]int64)
	var mu sync.Mutex
	deny := func() (ImageAuthorization, error) {
		return ImageAuthorization{}, &imaging.ImageError{Code: imaging.CodeForbidden}
	}
	return func(r *http.Request) (ImageAuthorization, error) {
		values := r.Header.Values(ImageDelegationHeader)
		if len(values) != 1 || len(values[0]) > 8192 {
			return deny()
		}
		parts := strings.Split(values[0], ".")
		if len(parts) != 4 || parts[0] != "v1" {
			return deny()
		}
		key, ok := keys[parts[1]]
		if !ok {
			return deny()
		}
		signature, err := base64.RawURLEncoding.Strict().DecodeString(parts[3])
		if err != nil || !ed25519.Verify(key.PublicKey, []byte(imageDelegationDomain+parts[1]+"."+parts[2]), signature) {
			return deny()
		}
		payload, err := base64.RawURLEncoding.Strict().DecodeString(parts[2])
		if err != nil {
			return deny()
		}
		decoder := json.NewDecoder(bytes.NewReader(payload))
		decoder.DisallowUnknownFields()
		var claims imageDelegationClaims
		if decoder.Decode(&claims) != nil {
			return deny()
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			return deny()
		}
		timestamp := now().Unix()
		nonce, err := base64.RawURLEncoding.Strict().DecodeString(claims.Nonce)
		if err != nil || len(nonce) < 16 || len(nonce) > 64 || strings.TrimSpace(claims.Subject) == "" || len(claims.Subject) > 512 || claims.BackendID != cfg.BackendID || claims.Audience != "image" || claims.IssuedAt > timestamp || claims.ExpiresAt <= timestamp || claims.IssuedAt < timestamp-60 || claims.ExpiresAt-claims.IssuedAt > 60 || claims.ExpiresAt <= claims.IssuedAt || (claims.CanConfigure && !key.AllowConfigure) {
			return deny()
		}
		// Across all trusted keys, nonce reuse is denied for the entire valid window.
		mu.Lock()
		defer mu.Unlock()
		for nonce, expiry := range nonces {
			if expiry <= timestamp {
				delete(nonces, nonce)
			}
		}
		if _, seen := nonces[claims.Nonce]; seen || len(nonces) >= capacity {
			return deny()
		}
		nonces[claims.Nonce] = claims.ExpiresAt
		return ImageAuthorization{Scope: imaging.Scope{OwnerID: claims.Subject, BackendID: claims.BackendID}, CanConfigure: claims.CanConfigure}, nil
	}, nil
}
