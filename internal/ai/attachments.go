package ai

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
)

// AttachmentKind names the provider-neutral attachment classes.
const (
	AttachmentImage = "image"
	AttachmentFile  = "file"
)

func FilePart(mimeType, filename string, data []byte) ContentPart {
	return ContentPart{Type: AttachmentFile, MimeType: mimeType, FileURL: &struct {
		URL      string `json:"url"`
		Filename string `json:"filename,omitempty"`
	}{URL: "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data), Filename: filename}}
}

func AttachmentData(p ContentPart) (mimeType, value string, isData bool, err error) {
	var raw string
	switch p.Type {
	case "image_url":
		if p.ImageURL == nil {
			return "", "", false, fmt.Errorf("image attachment requires image_url")
		}
		raw = p.ImageURL.URL
	case AttachmentFile:
		if p.FileURL == nil {
			return "", "", false, fmt.Errorf("file attachment requires file_url")
		}
		raw = p.FileURL.URL
	default:
		return "", "", false, fmt.Errorf("unsupported attachment type %q", p.Type)
	}
	if strings.HasPrefix(raw, "data:") {
		header, data, ok := strings.Cut(strings.TrimPrefix(raw, "data:"), ";base64,")
		if !ok || header == "" || data == "" {
			return "", "", false, fmt.Errorf("attachment data URL must contain a media type and base64 data")
		}
		if _, err := base64.StdEncoding.DecodeString(data); err != nil {
			return "", "", false, fmt.Errorf("attachment data URL contains invalid base64")
		}
		return header, data, true, nil
	}
	u, parseErr := url.Parse(raw)
	if parseErr != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", "", false, fmt.Errorf("attachment URL must use HTTP, HTTPS, or base64 data")
	}
	return p.MimeType, raw, false, nil
}

func (p ContentPart) Attachment() bool { return p.Type == "image_url" || p.Type == AttachmentFile }

func validateGeminiAttachments(messages []Message) error {
	if err := validateAttachments(messages, false, "Gemini"); err != nil {
		return err
	}
	for _, message := range messages {
		for _, part := range message.Parts {
			if !part.Attachment() {
				continue
			}
			if part.Type == "image_url" {
				_, _, isData, err := AttachmentData(part)
				if err != nil {
					return fmt.Errorf("Gemini attachment: %w", err)
				}
				if !isData {
					return fmt.Errorf("Gemini supports only inline base64 image attachments")
				}
			}
		}
	}
	return nil
}

func validateAttachments(messages []Message, files bool, provider string) error {
	for _, message := range messages {
		for _, part := range message.Parts {
			if !part.Attachment() {
				continue
			}
			if part.Type == AttachmentFile && !files {
				return fmt.Errorf("%s does not support file/PDF/document attachments", provider)
			}
			if _, _, _, err := AttachmentData(part); err != nil {
				return fmt.Errorf("%s attachment: %w", provider, err)
			}
			if part.Type == "image_url" && part.MimeType != "" && !strings.HasPrefix(part.MimeType, "image/") {
				return fmt.Errorf("%s does not support non-image image attachments", provider)
			}
		}
	}
	return nil
}
