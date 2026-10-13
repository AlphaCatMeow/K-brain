package backend

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

func delegationTestKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}
func delegationTestToken(t *testing.T, key ed25519.PrivateKey, claims imageDelegationClaims) string {
	t.Helper()
	data, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	unsigned := "test." + base64.RawURLEncoding.EncodeToString(data)
	return "v1." + unsigned + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte(imageDelegationDomain+unsigned)))
}
func delegationTestClaims() imageDelegationClaims {
	return imageDelegationClaims{Subject: "alice", BackendID: "backend-a", IssuedAt: 1000, ExpiresAt: 1060, Audience: "image", Nonce: base64.RawURLEncoding.EncodeToString(make([]byte, 24))}
}

func TestImageDelegationValidation(t *testing.T) {
	pub, priv := delegationTestKey(t)
	_, forged := delegationTestKey(t)
	tests := []struct {
		name      string
		mutate    func(*imageDelegationClaims)
		sign      ed25519.PrivateKey
		configure bool
		good      bool
	}{
		{name: "valid", good: true},
		{name: "configuration grant", mutate: func(c *imageDelegationClaims) { c.CanConfigure = true }, configure: true, good: true},
		{name: "configuration ceiling", mutate: func(c *imageDelegationClaims) { c.CanConfigure = true }},
		{name: "expired", mutate: func(c *imageDelegationClaims) { c.ExpiresAt = 1000 }},
		{name: "future", mutate: func(c *imageDelegationClaims) { c.IssuedAt = 1001 }},
		{name: "long lifetime", mutate: func(c *imageDelegationClaims) { c.ExpiresAt = 1061 }},
		{name: "old issued", mutate: func(c *imageDelegationClaims) { c.IssuedAt = 939 }},
		{name: "wrong audience", mutate: func(c *imageDelegationClaims) { c.Audience = "chat" }},
		{name: "wrong backend", mutate: func(c *imageDelegationClaims) { c.BackendID = "backend-b" }},
		{name: "missing owner", mutate: func(c *imageDelegationClaims) { c.Subject = " " }},
		{name: "short nonce", mutate: func(c *imageDelegationClaims) { c.Nonce = "YQ" }},
		{name: "forged signature", sign: forged},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver, err := NewImageDelegationResolver(ImageDelegationConfig{BackendID: "backend-a", Keys: map[string]ImageDelegationKey{"test": {pub, tt.configure}}, Now: func() time.Time { return time.Unix(1000, 0) }})
			if err != nil {
				t.Fatal(err)
			}
			claims := delegationTestClaims()
			if tt.mutate != nil {
				tt.mutate(&claims)
			}
			sign := priv
			if tt.sign != nil {
				sign = tt.sign
			}
			req := httptest.NewRequest("GET", "/v1/image/assets/ia_other/content", nil)
			req.Header.Set(ImageDelegationHeader, delegationTestToken(t, sign, claims))
			req.Header.Set("X-Owner-ID", "mallory")
			req.Header.Set("X-Backend-ID", "backend-b")
			result, err := resolver(req)
			if tt.good {
				if err != nil || result.Scope.OwnerID != "alice" || result.Scope.BackendID != "backend-a" || result.CanConfigure != tt.configure {
					t.Fatalf("result=%+v error=%v", result, err)
				}
			} else if err == nil {
				t.Fatal("invalid delegation accepted")
			}
		})
	}
}

func TestImageDelegationReplayConcurrentAndCapacity(t *testing.T) {
	pub, priv := delegationTestKey(t)
	var now atomic.Int64
	now.Store(1000)
	resolver, err := NewImageDelegationResolver(ImageDelegationConfig{BackendID: "backend-a", Keys: map[string]ImageDelegationKey{"test": {PublicKey: pub}}, Now: func() time.Time { return time.Unix(now.Load(), 0) }, MaxNonces: 1})
	if err != nil {
		t.Fatal(err)
	}
	claims := delegationTestClaims()
	token := delegationTestToken(t, priv, claims)
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := httptest.NewRequest("GET", "/v1/image/targets", nil)
			r.Header.Set(ImageDelegationHeader, token)
			if _, err := resolver(r); err == nil {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatalf("accepted %d replays", accepted.Load())
	}
	claims.Nonce = base64.RawURLEncoding.EncodeToString([]byte("different nonce with 24 bytes"))
	r := httptest.NewRequest("GET", "/v1/image/targets", nil)
	r.Header.Set(ImageDelegationHeader, delegationTestToken(t, priv, claims))
	if _, err := resolver(r); err == nil {
		t.Fatal("full nonce cache admitted request")
	}
	now.Store(1060)
	claims.IssuedAt = 1060
	claims.ExpiresAt = 1120
	r.Header.Set(ImageDelegationHeader, delegationTestToken(t, priv, claims))
	if _, err := resolver(r); err != nil {
		t.Fatal("expired nonce was not reclaimed", err)
	}
}

func TestImageDelegationHTTPAssetOwnerAndConfiguration(t *testing.T) {
	s := imageHTTPFixture(t)
	pub, priv := delegationTestKey(t)
	resolver, err := NewImageDelegationResolver(ImageDelegationConfig{BackendID: "backend-a", Keys: map[string]ImageDelegationKey{"test": {PublicKey: pub, AllowConfigure: true}}, Now: func() time.Time { return time.Unix(1000, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	s.image.ResolveScope = resolver
	s.image.AllowLocalImageScope = false
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	asset, err := s.image.Assets.ImportAsset(context.Background(), imaging.Scope{OwnerID: "alice", BackendID: "backend-a"}, bytes.NewReader(data.Bytes()), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	request := func(owner, path string, configure bool, bearer string) *httptest.ResponseRecorder {
		claims := delegationTestClaims()
		claims.Subject = owner
		claims.CanConfigure = configure
		nonce := make([]byte, 24)
		if _, err := rand.Read(nonce); err != nil {
			t.Fatal(err)
		}
		claims.Nonce = base64.RawURLEncoding.EncodeToString(nonce)
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", bearer)
		req.Header.Set(ImageDelegationHeader, delegationTestToken(t, priv, claims))
		req.Header.Set("X-Owner-ID", "alice")
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		return rec
	}
	path := "/v1/image/assets/" + asset.ID + "/content"
	if rec := request("alice", path, false, "Bearer test-image-token"); rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), data.Bytes()) {
		t.Fatalf("own asset %d %s", rec.Code, rec.Body.String())
	}
	imageAssertError(t, request("bob", path, false, "Bearer test-image-token"), 404, imaging.CodeNotFound)
	imageAssertError(t, request("alice", path, false, "Bearer wrong"), 401, imaging.CodeAuthRequired)
	imageAssertError(t, request("alice", "/v1/image/settings", false, "Bearer test-image-token"), 403, imaging.CodeForbidden)
	// A signed admin grant does not bypass the local-only settings manager.
	imageAssertError(t, request("alice", "/v1/image/settings", true, "Bearer test-image-token"), 403, imaging.CodeForbidden)
}

func TestImageDelegationRejectMissingTrustAndHeaders(t *testing.T) {
	if _, err := NewImageDelegationResolver(ImageDelegationConfig{}); err == nil {
		t.Fatal("missing trust accepted")
	}
	pub, priv := delegationTestKey(t)
	resolver, err := NewImageDelegationResolver(ImageDelegationConfig{BackendID: "backend-a", Keys: map[string]ImageDelegationKey{"test": {PublicKey: pub}}, Now: func() time.Time { return time.Unix(1000, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/v1/image/targets", nil)
	r.Header.Set("X-Owner-ID", "local")
	if _, err := resolver(r); err == nil {
		t.Fatal("browser scope accepted")
	}
	token := delegationTestToken(t, priv, delegationTestClaims())
	r.Header.Add(ImageDelegationHeader, token)
	r.Header.Add(ImageDelegationHeader, token)
	if _, err := resolver(r); err == nil {
		t.Fatal("duplicate delegation accepted")
	}
}
