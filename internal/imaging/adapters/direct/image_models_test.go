package direct

import (
	"context"
	"net/http"
	"testing"
)

func TestProviderImageAliasesRequireProvenanceAndSafeModelPath(t *testing.T) {
	for _, id := range []string{OpenAIID, GoogleID} {
		a := adapter(t, id, options(&http.Client{}, "https://image.example/v1"))
		r := request(id)
		r.Target.RemoteID = "private-painter"
		if _, _, err := a.request(context.Background(), r); err == nil {
			t.Fatal("unregistered alias accepted by legacy profile")
		}
		r.Target.Source = "provider"
		if _, _, err := a.request(context.Background(), r); err != nil {
			t.Fatal("configured provider alias rejected", err)
		}
		for _, unsafe := range []string{"../chat", "model?key=secret", "models/painter", "model%2fother", "model:generateContent", ""} {
			r.Target.RemoteID = unsafe
			if _, _, err := a.request(context.Background(), r); err == nil {
				t.Fatalf("unsafe model accepted: %q", unsafe)
			}
		}
	}
}
