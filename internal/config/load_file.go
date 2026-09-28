package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// SaveFile atomically replaces the explicit backend configuration, including an empty catalog.
func (c *Config) SaveFile(filename string) error {
	data, err := marshalConfig(c)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(filename), ".config-*")
	if err != nil {
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filename)
}

// Path returns the default configuration file path.
func Path() (string, error) { return path() }

// LoadFile reads a configuration without changing the user's default file.
func LoadFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := parseConfigJSONC(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &cfg, nil
}
