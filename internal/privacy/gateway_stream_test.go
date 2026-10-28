package privacy

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

func TestTransportStreamsBeforeResponseEnds(t *testing.T) {
	SetEnabled(true)
	t.Cleanup(func() { SetEnabled(false) })
	_, values := MaskText("alice@example.com")
	var token string
	for token = range values {
		break
	}

	for _, tc := range []struct {
		name   string
		chunks []string
		want   string
	}{
		{"ordinary SSE event", []string{"data: early-text\n\n"}, "data: early-text\n\n"},
		{"short text", []string{"hi"}, "hi"},
		{"complete token", []string{token}, "alice@example.com"},
		{"split token", []string{token[:len(token)/2], token[len(token)/2:]}, "alice@example.com"},
		{"token SSE event", []string{"data: " + token, "\n\n"}, "data: alice@example.com\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src, writer := io.Pipe()
			t.Cleanup(func() { _ = src.Close(); _ = writer.Close() })
			base := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: src, Request: r}, nil
			})
			req, _ := http.NewRequest(http.MethodPost, "https://example.test", strings.NewReader(`{"message":"no secret"}`))
			resp, err := (&Transport{Base: base}).RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = resp.Body.Close() })
			written := make(chan error, 1)
			go func() {
				for _, chunk := range tc.chunks {
					if _, err := io.WriteString(writer, chunk); err != nil {
						written <- err
						return
					}
				}
				written <- nil
				// Keep the response open until cleanup, as an SSE provider does.
			}()
			type result struct {
				text string
				err  error
			}
			read := make(chan result, 1)
			go func() {
				buf := make([]byte, len(tc.want))
				_, err := io.ReadFull(resp.Body, buf)
				read <- result{string(buf), err}
			}()
			select {
			case got := <-read:
				if got.err != nil || got.text != tc.want {
					t.Fatalf("read = %q, %v; want %q", got.text, got.err, tc.want)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("response text was buffered until the next event or EOF")
			}
			if err := <-written; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRestoreReaderChunkBoundaries(t *testing.T) {
	values := map[string]string{
		"__KBR_EMAIL_0001__": "alice@example.com",
		"__KBR_PHONE_0002__": "13812345678",
	}
	input := "__KBR_EMAIL_0001____KBR_PHONE_0002__\n\npartial __KBR_EMAIL_00"
	want := "alice@example.com13812345678\n\npartial __KBR_EMAIL_00"
	for split := 0; split <= len(input); split++ {
		src := io.MultiReader(strings.NewReader(input[:split]), strings.NewReader(input[split:]))
		r := &restoreReader{src: io.NopCloser(src), values: values}
		var out strings.Builder
		buf := make([]byte, 1)
		if _, err := io.CopyBuffer(&out, r, buf); err != nil {
			t.Fatal(err)
		}
		if out.String() != want {
			t.Fatalf("split %d: got %q, want %q", split, out.String(), want)
		}
	}
}

func TestRestoreReaderDrainsBeforeSourceError(t *testing.T) {
	wantErr := errors.New("stream interrupted")
	src := &dataErrorReader{data: "__KBR_EMAIL_0001__ partial __KBR_", err: wantErr}
	r := &restoreReader{src: src, values: map[string]string{"__KBR_EMAIL_0001__": "alice@example.com"}}
	out, err := io.ReadAll(r)
	if string(out) != "alice@example.com partial __KBR_" || !errors.Is(err, wantErr) {
		t.Fatalf("read = %q, %v", out, err)
	}
}

func TestRestoreReaderHandlesBytewiseTokens(t *testing.T) {
	values := map[string]string{
		"__KBR_EMAIL_0001__": "__KBR_PHONE_0002__",
		"__KBR_PHONE_0002__": "13812345678",
	}
	input := "__KBR_EMAIL_0001____KBR_PHONE_0002__\n\n"
	r := &restoreReader{src: io.NopCloser(iotest.OneByteReader(strings.NewReader(input))), values: values}
	if n, err := r.Read(nil); n != 0 || err != nil {
		t.Fatalf("empty read = %d, %v", n, err)
	}
	out, err := io.ReadAll(r)
	if string(out) != "__KBR_PHONE_0002__13812345678\n\n" || err != nil {
		t.Fatalf("read = %q, %v", out, err)
	}
}

type dataErrorReader struct {
	data string
	err  error
}

func (r *dataErrorReader) Read(p []byte) (int, error) {
	n := copy(p, r.data)
	r.data = r.data[n:]
	if len(r.data) == 0 {
		return n, r.err
	}
	return n, nil
}

func (r *dataErrorReader) Close() error { return nil }
