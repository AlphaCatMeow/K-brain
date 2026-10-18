package direct

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/textproto"
	"strconv"

	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

// A bounded in-memory body has no pipe goroutine or retryable GetBody. File bytes
// have already passed image decoding, MIME, per-image and pixel checks in inputs.
type imageRequestBuffer struct {
	bytes.Buffer
	max int64
}

func (b *imageRequestBuffer) Write(p []byte) (int, error) {
	if int64(b.Len())+int64(len(p)) > b.max {
		return 0, failure(imaging.CodeTooLarge, imaging.DeliveryNotSent)
	}
	return b.Buffer.Write(p)
}

func (a *Adapter) encodeRequest(ctx context.Context, body any) ([]byte, string, error) {
	edit, ok := body.(openAIEditRequest)
	if !ok {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, "", badInput()
		}
		if int64(len(raw)) > a.limits.MaxRequestBytes {
			return nil, "", failure(imaging.CodeTooLarge, imaging.DeliveryNotSent)
		}
		return raw, "application/json", nil
	}
	var b imageRequestBuffer
	b.max = a.limits.MaxRequestBytes
	writer := multipart.NewWriter(&b)
	fields := []struct{ name, value string }{
		{"model", edit.Fields.Model}, {"prompt", edit.Fields.Prompt}, {"n", strconv.Itoa(edit.Fields.N)},
		{"size", edit.Fields.Size}, {"quality", edit.Fields.Quality}, {"output_format", edit.Fields.Format}, {"background", edit.Fields.Background},
	}
	if edit.Fields.Compression != nil {
		fields = append(fields, struct{ name, value string }{"output_compression", strconv.Itoa(*edit.Fields.Compression)})
	}
	for _, field := range fields {
		if field.value != "" {
			if err := writer.WriteField(field.name, field.value); err != nil {
				return nil, "", failure(imaging.CodeTooLarge, imaging.DeliveryNotSent)
			}
		}
	}
	writeImage := func(name, filename string, image imaging.ImageContent) error {
		if ctx.Err() != nil {
			return failure(imaging.CodeUnavailable, imaging.DeliveryNotSent)
		}
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, name, filename))
		header.Set("Content-Type", image.MIME)
		part, err := writer.CreatePart(header)
		if err != nil {
			return failure(imaging.CodeTooLarge, imaging.DeliveryNotSent)
		}
		if _, err := part.Write(image.Data); err != nil {
			return failure(imaging.CodeTooLarge, imaging.DeliveryNotSent)
		}
		return nil
	}
	for i, image := range edit.Images {
		extension := map[string]string{"image/png": "png", "image/jpeg": "jpg", "image/webp": "webp"}[image.MIME]
		if err := writeImage("image[]", fmt.Sprintf("image-%d.%s", i+1, extension), image); err != nil {
			return nil, "", err
		}
	}
	if edit.Mask != nil {
		if err := writeImage("mask", "mask.png", *edit.Mask); err != nil {
			return nil, "", err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, "", failure(imaging.CodeTooLarge, imaging.DeliveryNotSent)
	}
	return b.Bytes(), writer.FormDataContentType(), nil
}
