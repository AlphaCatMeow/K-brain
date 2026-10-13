package backend

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"

	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

const imageUploadBytes int64 = 20 << 20

func (s *Server) imageAssets(w http.ResponseWriter, r *http.Request, scope imaging.Scope, parts []string) {
	assets := s.image.Assets
	if assets == nil {
		imageHTTPError(w, r, imageError(imaging.CodeUnavailable))
		return
	}
	if len(parts) == 1 {
		if !imageMethod(w, r, http.MethodPost) {
			return
		}
		// No multipart temporary files, no caller filenames used as disk paths. Bound
		// both the request overhead and the one image before committing any asset.
		r.Body = http.MaxBytesReader(w, r.Body, imageUploadBytes+(64<<10))
		reader, err := r.MultipartReader()
		if err != nil {
			imageHTTPError(w, r, imageError(imaging.CodeInvalidInput))
			return
		}
		part, err := reader.NextPart()
		if err != nil {
			imageUploadError(w, r, err)
			return
		}
		if part.FormName() != "file" || part.FileName() == "" {
			imageHTTPError(w, r, imageError(imaging.CodeInvalidInput))
			return
		}
		data, err := io.ReadAll(io.LimitReader(part, imageUploadBytes+1))
		if err != nil {
			imageUploadError(w, r, err)
			return
		}
		if int64(len(data)) > imageUploadBytes {
			imageHTTPError(w, r, imageError(imaging.CodeTooLarge))
			return
		}
		declared := part.Header.Get("Content-Type")
		if _, err = reader.NextPart(); err != io.EOF {
			if err == nil {
				err = imageError(imaging.CodeInvalidInput)
			}
			imageUploadError(w, r, err)
			return
		}
		// Read any epilogue too, so MaxBytesReader covers the complete HTTP body.
		if _, err = io.Copy(io.Discard, r.Body); err != nil {
			imageUploadError(w, r, err)
			return
		}
		asset, err := assets.ImportAsset(r.Context(), scope, bytes.NewReader(data), declared)
		if err != nil {
			imageHTTPError(w, r, err)
			return
		}
		imageJSON(w, r, http.StatusCreated, map[string]any{"asset": imaging.PublicAsset(asset)})
		return
	}
	if len(parts) != 3 || parts[1] == "" || parts[2] != "content" {
		imageHTTPError(w, r, imageError(imaging.CodeNotFound))
		return
	}
	if !imageMethod(w, r, http.MethodGet, http.MethodHead) {
		return
	}
	variant := r.URL.Query().Get("variant")
	if variant == "" {
		variant = "original"
	}
	if variant != "original" && variant != "thumbnail" {
		imageHTTPError(w, r, imageError(imaging.CodeInvalidInput))
		return
	}
	reader, asset, err := assets.OpenAsset(r.Context(), scope, parts[1], variant)
	if err != nil {
		imageHTTPError(w, r, err)
		return
	}
	defer reader.Close()
	w.Header().Set("Content-Type", asset.MIME)
	w.Header().Set("Content-Length", strconv.FormatInt(asset.Bytes, 10))
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = io.Copy(w, imageContextReader{r.Context(), reader})
}
func imageUploadError(w http.ResponseWriter, r *http.Request, err error) {
	// Multipart syntax diagnostics can contain caller data; the public envelope
	// must never include them. Retain only the explicit request size error.
	if !isImageBodyTooLarge(err) {
		err = imageError(imaging.CodeInvalidInput)
	}
	imageHTTPError(w, r, err)
}

type imageContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r imageContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
