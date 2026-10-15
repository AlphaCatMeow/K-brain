package imaging

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func imageTestBytes(t *testing.T, format string, width, height int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	img.SetNRGBA(0, 0, color.NRGBA{R: 220, G: 30, B: 40, A: 255})
	var b bytes.Buffer
	var err error
	if format == "jpeg" {
		err = jpeg.Encode(&b, img, nil)
	} else {
		err = png.Encode(&b, img)
	}
	if err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestImageContentPreservesOriginalAndMakesThumbnail(t *testing.T) {
	for _, format := range []string{"png", "jpeg"} {
		t.Run(format, func(t *testing.T) {
			raw := imageTestBytes(t, format, 1024, 512)
			got, err := DecodeImageContent(context.Background(), bytes.NewReader(raw), "image/"+format, UploadImageLimits)
			if err != nil {
				t.Fatal(err)
			}
			hash := sha256.Sum256(raw)
			if got.Hash != hex.EncodeToString(hash[:]) || !bytes.Equal(got.Data, raw) || got.Width != 1024 || got.Height != 512 {
				t.Fatal("original image changed")
			}
			thumb, f, err := image.Decode(bytes.NewReader(got.Thumbnail))
			if err != nil || f != "png" {
				t.Fatalf("thumbnail: %s %v", f, err)
			}
			if thumb.Bounds().Dx() != 512 || thumb.Bounds().Dy() != 256 {
				t.Fatal("thumbnail did not preserve aspect ratio")
			}
		})
	}
}

func TestImageContentRejectsInvalidUploads(t *testing.T) {
	raw := imageTestBytes(t, "png", 20, 10)
	cases := []struct {
		name   string
		data   []byte
		mime   string
		limits ImageLimits
	}{
		{"empty", nil, "image/png", UploadImageLimits},
		{"text", []byte("not an image"), "image/png", UploadImageLimits},
		{"mismatch", raw, "image/jpeg", UploadImageLimits},
		{"unsupported", raw, "image/svg+xml", UploadImageLimits},
		{"truncated", raw[:len(raw)/2], "image/png", UploadImageLimits},
		{"bytes", raw, "image/png", ImageLimits{MaxBytes: int64(len(raw) - 1), MaxPixels: 400}},
		{"pixels", raw, "image/png", ImageLimits{MaxBytes: 10000, MaxPixels: 100}},
		{"invalid limits", raw, "image/png", ImageLimits{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeImageContent(context.Background(), bytes.NewReader(tc.data), tc.mime, tc.limits); err == nil {
				t.Fatal("accepted invalid image")
			}
		})
	}
}

func TestImageContentWebP(t *testing.T) {
	// Synthetic 2x3 solid-color lossless WebP.
	raw, err := base64.StdEncoding.DecodeString("UklGRh4AAABXRUJQVlA4TBEAAAAvAYAAAAdQj+KVp/+BiOh/AAA=")
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeImageContent(context.Background(), bytes.NewReader(raw), "image/webp", UploadImageLimits)
	if err != nil {
		t.Fatal(err)
	}
	if got.MIME != "image/webp" || got.Width != 2 || got.Height != 3 || !bytes.Equal(got.Data, raw) {
		t.Fatal("WebP changed")
	}
	if _, err := DecodeImageContent(context.Background(), bytes.NewReader(raw[:len(raw)-8]), "image/webp", UploadImageLimits); err == nil {
		t.Fatal("truncated WebP accepted")
	}
}

func TestImageContentCancellationAndSmallThumbnail(t *testing.T) {
	raw := imageTestBytes(t, "png", 2, 3)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := DecodeImageContent(ctx, bytes.NewReader(raw), "image/png", UploadImageLimits); err != context.Canceled {
		t.Fatalf("cancel error: %v", err)
	}
	got, err := DecodeImageContent(context.Background(), bytes.NewReader(raw), "", UploadImageLimits)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(got.Thumbnail))
	if err != nil || cfg.Width != 2 || cfg.Height != 3 {
		t.Fatal("small image enlarged")
	}
}
