package config

import (
	"strings"
	"testing"
)

func TestParseConfigRejectsConflictingSharedModelMetadata(t *testing.T) {
	input := `{"providers":{"a":{"api":"openai-completions","baseUrl":"https://a.invalid/v1","models":[{"id":"shared","contextWindow":100}]},"b":{"api":"openai-completions","baseUrl":"https://b.invalid/v1","models":[{"id":"shared","contextWindow":200}]}}}`
	var cfg Config
	err := parseConfigJSONC([]byte(input), &cfg)
	if err == nil || !strings.Contains(err.Error(), "conflicting metadata") {
		t.Fatalf("error = %v", err)
	}
}

func TestMarshalConfigRejectsConflictingSharedModelMetadata(t *testing.T) {
	cfg := &Config{Providers: map[string]Provider{
		"a": {API: "openai-completions", BaseURL: "https://a.invalid/v1", Models: []PiModel{{ID: "shared", ContextWindow: 100}}},
		"b": {API: "openai-completions", BaseURL: "https://b.invalid/v1", Models: []PiModel{{ID: "shared", ContextWindow: 200}}},
	}, Models: map[string]Model{"shared": {ID: "shared", Providers: []string{"a", "b"}, Context: 100}}}
	if _, err := marshalConfig(cfg); err == nil || !strings.Contains(err.Error(), "conflicting metadata") {
		t.Fatalf("error = %v", err)
	}
}
