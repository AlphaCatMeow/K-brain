package ai

import (
	"net/http"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/netproxy"
	"github.com/Stack-Cairn/K-brain/internal/privacy"
)

func TestNewClientSelectsProxyTransport(t *testing.T) {
	for _, api := range []string{APIChatCompletions, APIResponses, APIMessages, APIGemini} {
		for _, useProxy := range []bool{true, false} {
			client, err := NewClient(ClientOptions{API: api, BaseURL: "https://provider.test/v1", APIKey: "k", UseProxy: useProxy})
			if err != nil {
				t.Fatal(err)
			}
			var h *http.Client
			switch c := client.(type) {
			case *OpenAI:
				h = c.HTTP
			case *Responses:
				h = c.HTTP
			case *Anthropic:
				h = c.HTTP
			case *Gemini:
				h = c.HTTP
			default:
				t.Fatalf("%s: unexpected client %T", api, client)
			}
			rt := h.Transport
			if wrapped, ok := rt.(*privacy.Transport); ok {
				rt = wrapped.Base
			}
			if rt != netproxy.Transport(useProxy) {
				t.Fatalf("%s useProxy=%v: wrong transport %T", api, useProxy, rt)
			}
		}
	}
}
