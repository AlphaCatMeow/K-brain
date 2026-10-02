package protocol

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
)

func validateAttachmentURL(raw, kind string) error {
	if strings.HasPrefix(raw, "data:") {
		header, data, ok := strings.Cut(strings.TrimPrefix(raw, "data:"), ";base64,")
		if !ok || header == "" || data == "" {
			return fmt.Errorf("%s attachment data URL must contain a media type and base64 data", kind)
		}
		if _, err := base64.StdEncoding.DecodeString(data); err != nil {
			return fmt.Errorf("%s attachment data URL contains invalid base64", kind)
		}
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return fmt.Errorf("%s attachment URL must use HTTP, HTTPS, or base64 data", kind)
	}
	return nil
}
