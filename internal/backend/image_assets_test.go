package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"mime/multipart"
	"net/http/httptest"
	"net/textproto"
	"strconv"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

func imageHTTPUpload(s *Server, data []byte, count int) *httptest.ResponseRecorder {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for range count {
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", `form-data; name="file"; filename="../../image.png"`)
		header.Set("Content-Type", "image/png")
		part, _ := writer.CreatePart(header)
		_, _ = part.Write(data)
	}
	_ = writer.Close()
	r := httptest.NewRequest("POST", "/v1/image/assets", &body)
	r.RemoteAddr = "127.0.0.1:1"
	r.Header.Set("Authorization", "Bearer test-image-token")
	r.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func TestImageAssetHTTPUploadReadHeadLimits(t *testing.T) {
	s := imageHTTPFixture(t)
	var original bytes.Buffer
	if err := png.Encode(&original, image.NewRGBA(image.Rect(0, 0, 3, 2))); err != nil {
		t.Fatal(err)
	}
	w := imageHTTPUpload(s, original.Bytes(), 1)
	if w.Code != 201 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var result struct{ Asset imaging.ImageAsset }
	if json.Unmarshal(w.Body.Bytes(), &result) != nil {
		t.Fatal(w.Body.String())
	}
	id := result.Asset.ID
	for _, variant := range []string{"original", "thumbnail"} {
		path := "/v1/image/assets/" + id + "/content?variant=" + variant
		get := imageHTTPRequest(s, "GET", path, nil)
		head := imageHTTPRequest(s, "HEAD", path, nil)
		if get.Code != 200 || head.Code != 200 || head.Body.Len() != 0 || get.Header().Get("Content-Length") != strconv.Itoa(get.Body.Len()) || head.Header().Get("Content-Length") != get.Header().Get("Content-Length") || !strings.HasPrefix(get.Header().Get("Cache-Control"), "private") {
			t.Fatalf("invalid binary response %d %s", get.Code, get.Body.String())
		}
		if get.Header().Get("X-Image-Version") != imaging.WireVersion {
			t.Fatal("binary version missing")
		}
		if variant == "original" && !bytes.Equal(get.Body.Bytes(), original.Bytes()) {
			t.Fatal("original changed")
		}
	}
	w = imageHTTPUpload(s, original.Bytes(), 2)
	imageAssertError(t, w, 422, imaging.CodeInvalidInput)
	w = imageHTTPUpload(s, []byte("not a png"), 1)
	imageAssertError(t, w, 422, imaging.CodeInvalidInput)
	w = imageHTTPUpload(s, make([]byte, imageUploadBytes+1), 1)
	imageAssertError(t, w, 413, imaging.CodeTooLarge)
	assets, err := s.image.Store.ListAssets(context.Background(), imageTestScope(), imaging.PageRequest{Limit: 100})
	if err != nil || len(assets.Items) != 1 {
		t.Fatalf("rejected uploads persisted: %v %v", assets, err)
	}
	foreign, err := s.image.Assets.ImportAsset(context.Background(), imaging.Scope{OwnerID: "other", BackendID: "local"}, bytes.NewReader(original.Bytes()), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	w = imageHTTPRequest(s, "GET", "/v1/image/assets/"+foreign.ID+"/content", nil)
	imageAssertError(t, w, 404, imaging.CodeNotFound)
	w = imageHTTPRequest(s, "GET", "/v1/image/assets/"+id+"/content?variant=../original", nil)
	imageAssertError(t, w, 422, imaging.CodeInvalidInput)
}
