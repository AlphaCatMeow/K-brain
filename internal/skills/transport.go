package skills

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// SetHTTPClient is an explicit fixture/deployment seam. The default transport only accepts HTTPS.
func (m *Manager) SetHTTPClient(client *http.Client) error {
	if client == nil {
		return errors.New("HTTP client is required")
	}
	m.jobsMu.Lock()
	m.client = client
	m.allowInsecureHTTP = true
	m.jobsMu.Unlock()
	return nil
}

func validateRemoteURL(raw string, allowInsecure bool) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return nil, errors.New("invalid source URL")
	}
	if u.Scheme != "https" && !(allowInsecure && (u.Scheme == "http" || strings.EqualFold(u.Scheme, "https"))) {
		return nil, errors.New("remote skill sources require HTTPS")
	}
	return u, nil
}
