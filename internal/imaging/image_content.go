package imaging

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"image"
	_ "image/jpeg"
	"image/png"
	"io"
	"mime"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

type ImageLimits struct {
	MaxBytes  int64
	MaxPixels int64
}

var UploadImageLimits = ImageLimits{MaxBytes: 20 << 20, MaxPixels: 40_000_000}
var OutputImageLimits = ImageLimits{MaxBytes: 100 << 20, MaxPixels: 100_000_000}

// ImageContent contains validated original bytes and a PNG thumbnail.
type ImageContent struct {
	MIME      string
	Hash      string
	Width     int
	Height    int
	Data      []byte
	Thumbnail []byte
}

func DecodeImageContent(ctx context.Context, input io.Reader, declaredMIME string, limits ImageLimits) (ImageContent, error) {
	if err := ctx.Err(); err != nil {
		return ImageContent{}, err
	}
	if input == nil || limits.MaxBytes < 1 || limits.MaxBytes > 100<<20 || limits.MaxPixels < 1 || limits.MaxPixels > 100_000_000 {
		return ImageContent{}, invalid("invalid image limits")
	}
	if declaredMIME != "" {
		mediaType, _, err := mime.ParseMediaType(declaredMIME)
		if err != nil || (mediaType != "image/png" && mediaType != "image/jpeg" && mediaType != "image/webp") {
			return ImageContent{}, invalid("unsupported image type")
		}
		declaredMIME = mediaType
	}
	raw, err := io.ReadAll(io.LimitReader(imageContextReader{ctx: ctx, r: input}, limits.MaxBytes+1))
	if err != nil {
		return ImageContent{}, err
	}
	if len(raw) == 0 {
		return ImageContent{}, invalid("empty image")
	}
	if int64(len(raw)) > limits.MaxBytes {
		return ImageContent{}, &ImageError{Code: CodeTooLarge}
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return ImageContent{}, invalid("invalid image header")
	}
	detected := map[string]string{"png": "image/png", "jpeg": "image/jpeg", "webp": "image/webp"}[format]
	if detected == "" || (declaredMIME != "" && declaredMIME != detected) {
		return ImageContent{}, invalid("image type does not match its content")
	}
	if cfg.Width < 1 || cfg.Height < 1 || int64(cfg.Width) > limits.MaxPixels/int64(cfg.Height) {
		return ImageContent{}, invalid("image dimensions exceed limit")
	}
	if err := ctx.Err(); err != nil {
		return ImageContent{}, err
	}
	decoded, decodedFormat, err := image.Decode(bytes.NewReader(raw))
	if err != nil || decodedFormat != format {
		return ImageContent{}, invalid("invalid image data")
	}
	if decoded.Bounds().Dx() != cfg.Width || decoded.Bounds().Dy() != cfg.Height {
		return ImageContent{}, invalid("inconsistent image dimensions")
	}
	if err := ctx.Err(); err != nil {
		return ImageContent{}, err
	}
	width, height := cfg.Width, cfg.Height
	if width > 512 || height > 512 {
		if width >= height {
			height = max(1, height*512/width)
			width = 512
		} else {
			width = max(1, width*512/height)
			height = 512
		}
	}
	thumbnail := image.NewNRGBA(image.Rect(0, 0, width, height))
	draw.CatmullRom.Scale(thumbnail, thumbnail.Bounds(), decoded, decoded.Bounds(), draw.Src, nil)
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, thumbnail); err != nil {
		return ImageContent{}, invalid("unable to encode image thumbnail")
	}
	if err := ctx.Err(); err != nil {
		return ImageContent{}, err
	}
	hash := sha256.Sum256(raw)
	return ImageContent{MIME: detected, Hash: hex.EncodeToString(hash[:]), Width: cfg.Width, Height: cfg.Height, Data: raw, Thumbnail: encoded.Bytes()}, nil
}

type imageContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r imageContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
