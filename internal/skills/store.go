package skills

import (
	"errors"
	"net/url"
	"strings"
)

// SetStoreURL overrides the ClawHub-compatible registry endpoint for an explicitly configured fixture.
func (m *Manager) SetStoreURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Host == "" || u.Scheme != "https" {
		return errors.New("store URL must use HTTPS")
	}
	managedWriteMu.Lock()
	m.storeURL = strings.TrimRight(raw, "/")
	managedWriteMu.Unlock()
	return nil
}
