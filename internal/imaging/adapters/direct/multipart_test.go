package direct

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

type imageMultipartParts struct {
	Fields map[string]string
	Files  map[string][][]byte
	MIME   map[string][]string
}

func parseImageMultipart(t *testing.T, r *http.Request) imageMultipartParts {
	t.Helper()
	out := imageMultipartParts{Fields: map[string]string{}, Files: map[string][][]byte{}, MIME: map[string][]string{}}
	reader, err := r.MultipartReader()
	if err != nil {
		t.Error("expected multipart request", err)
		return out
	}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Error(err)
			break
		}
		raw, err := io.ReadAll(part)
		if err != nil {
			t.Error(err)
		}
		name := part.FormName()
		if part.FileName() == "" {
			out.Fields[name] = string(raw)
		} else {
			out.Files[name] = append(out.Files[name], raw)
			out.MIME[name] = append(out.MIME[name], part.Header.Get("Content-Type"))
		}
		part.Close()
	}
	return out
}

func TestOpenAIOfficialMultipartEditModelParametersAndFiles(t *testing.T) {
	for _, model := range []string{"gpt-image-1", "gpt-image-1.5", "gpt-image-2", "gpt-image-2.5"} {
		t.Run(model, func(t *testing.T) {
			first, second, mask := syntheticPNG(t, 8, 30, false), syntheticPNG(t, 8, 90, false), syntheticPNG(t, 8, 30, true)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != http.MethodPost || r.URL.Path != "/v1/images/edits" || r.Header.Get("Authorization") != "Bearer synthetic-key" || r.URL.RawQuery != "" || r.Header.Get("Idempotency-Key") != "" {
					t.Error("wrong edit route/auth/replay contract")
				}
				parts := parseImageMultipart(t, r)
				wantFields := map[string]string{"model": model, "prompt": "A synthetic ceramic bird", "n": "1", "size": "1536x1024", "quality": "high", "output_format": "webp", "output_compression": "80", "background": "transparent"}
				if !reflect.DeepEqual(parts.Fields, wantFields) {
					t.Errorf("wrong multipart fields: %#v", parts.Fields)
				}
				if !reflect.DeepEqual(parts.Files["image[]"], [][]byte{first, second}) || !reflect.DeepEqual(parts.Files["mask"], [][]byte{mask}) {
					t.Error("multipart changed image/mask bytes or reference ordering")
				}
				if !reflect.DeepEqual(parts.MIME["image[]"], []string{"image/png", "image/png"}) || !reflect.DeepEqual(parts.MIME["mask"], []string{"image/png"}) {
					t.Error("wrong file MIME")
				}
				io.WriteString(w, responseJSON(OpenAIID, first))
			}))
			defer server.Close()
			a := adapter(t, OpenAIID, options(server.Client(), server.URL+"/v1"))
			r := request(OpenAIID)
			r.Target.RemoteID = model
			r.Request.Operation = imaging.OperationMask
			r.Request.ParentAssetID = "source"
			r.Request.Parameters = map[string]any{"size": "1536x1024", "quality": "high", "output_format": "webp", "output_compression": 80, "background": "transparent"}
			var closed atomic.Int32
			addInput(&r, "source", "source", first, &closed)
			addInput(&r, "reference", "reference", second, &closed)
			addInput(&r, "mask", "mask", mask, &closed)
			result, err := a.Submit(context.Background(), r)
			if err != nil || result.State != "completed" || len(result.Outputs) != 1 || !bytes.Equal(result.Outputs[0].Data, first) || calls.Load() != 1 || closed.Load() != 3 {
				t.Fatalf("multipart edit result=%s calls=%d closed=%d err=%v", result.State, calls.Load(), closed.Load(), err)
			}
		})
	}
}

func TestOpenAIMultipartLimitAndNoReplay(t *testing.T) {
	var calls atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.GetBody != nil {
			t.Error("multipart request was replayable")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(responseJSON(OpenAIID, syntheticPNG(t, 4, 20, false))))}, nil
	})}
	o := options(client, "https://image.example/v1")
	o.Limits.MaxRequestBytes = 1024
	a := adapter(t, OpenAIID, o)
	r := request(OpenAIID)
	r.Request.Operation = imaging.OperationEdit
	r.Request.Prompt = strings.Repeat("x", 700)
	addInput(&r, "source", "source", syntheticPNG(t, 4, 20, false), nil)
	result, err := a.Submit(context.Background(), r)
	assertError(t, result, err, imaging.CodeTooLarge, imaging.DeliveryNotSent)
	if calls.Load() != 0 {
		t.Fatal("oversized multipart reached HTTP")
	}
	o.Limits.MaxRequestBytes = 0
	a = adapter(t, OpenAIID, o)
	if _, err := a.Submit(context.Background(), r); err != nil || calls.Load() != 1 {
		t.Fatalf("valid multipart failed: %v", err)
	}
}

func TestContractRollDekEditAndReferenceUseMultipartMaskRejected(t *testing.T) {
	source, ref1, ref2 := syntheticPNG(t, 8, 30, false), syntheticPNG(t, 8, 60, false), syntheticPNG(t, 8, 90, false)
	var calls atomic.Int32
	var wantFiles [][]byte
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.URL.String() != "https://rolldek.com/v1/images/edits" {
			t.Errorf("wrong RollDek edit endpoint %s", r.URL)
		}
		parts := parseImageMultipart(t, r)
		if !reflect.DeepEqual(parts.Fields, map[string]string{"model": "gpt-image-2.5", "prompt": "A synthetic ceramic bird", "n": "1"}) {
			t.Errorf("RollDek edit sent unexpected fields: %#v", parts.Fields)
		}
		if !reflect.DeepEqual(parts.Files["image[]"], wantFiles) || len(parts.Files["mask"]) != 0 {
			t.Error("RollDek edit changed image bytes or sent a mask")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(responseJSON(OpenAIID, source)))}, nil
	})}
	o := options(client, "https://rolldek.com/v1")
	o.Profile = "rolldek"
	a, err := NewOpenAI(o)
	if err != nil {
		t.Fatal(err)
	}
	edit := request(OpenAIID)
	edit.Target.RemoteID = "gpt-image-2.5"
	edit.Request.Operation = imaging.OperationEdit
	addInput(&edit, "source", "source", source, nil)
	wantFiles = [][]byte{source}
	if res, err := a.Submit(context.Background(), edit); err != nil || len(res.Outputs) != 1 {
		t.Fatalf("RollDek edit failed: %v", err)
	}
	reference := request(OpenAIID)
	reference.Target.RemoteID = "gpt-image-2.5"
	reference.Request.Operation = imaging.OperationReference
	addInput(&reference, "r1", "reference", ref1, nil)
	addInput(&reference, "r2", "reference", ref2, nil)
	wantFiles = [][]byte{ref1, ref2}
	if res, err := a.Submit(context.Background(), reference); err != nil || len(res.Outputs) != 1 {
		t.Fatalf("RollDek reference failed: %v", err)
	}
	mask := request(OpenAIID)
	mask.Target.RemoteID = "gpt-image-2.5"
	mask.Request.Operation = imaging.OperationMask
	addInput(&mask, "source", "source", source, nil)
	addInput(&mask, "mask", "mask", syntheticPNG(t, 8, 30, true), nil)
	if _, err := a.Submit(context.Background(), mask); err == nil {
		t.Fatal("RollDek mask edit accepted before it is verified")
	}
	if calls.Load() != 2 {
		t.Fatalf("expected 2 upstream calls, got %d", calls.Load())
	}
}
