package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeConfigResolvesRelativePathsAndDefaults(t *testing.T) {
	base := t.TempDir()
	config, err := normalizeConfig(Config{
		Workspace:  "project",
		SessionDir: "state/sessions",
		Skills:     []string{"skills"},
		Provider:   ProviderConfig{Type: "deepseek"},
	}, base)
	if err != nil {
		t.Fatalf("normalizeConfig(): %v", err)
	}
	if config.Workspace != filepath.Join(base, "project") || config.SessionDir != filepath.Join(base, "state", "sessions") || config.Skills[0] != filepath.Join(base, "skills") {
		t.Fatalf("paths not resolved: %+v", config)
	}
	if config.Provider.APIKey != "" || config.Provider.Model != "deepseek-v4-flash" {
		t.Fatalf("defaults not applied: %+v", config)
	}
}

func TestNewProviderOnlyAcceptsImplementedTypes(t *testing.T) {
	if _, err := newProvider(ProviderConfig{Type: "unknown", APIKey: "test"}); err == nil {
		t.Fatal("unknown provider unexpectedly accepted")
	}
	if _, err := newProvider(ProviderConfig{Type: "openai-compatible", APIKey: "test", Model: "gpt-test"}); err == nil {
		t.Fatal("openai-compatible provider without base_url unexpectedly accepted")
	}
	if _, err := newProvider(ProviderConfig{Type: "openai-compatible", APIKey: "test", Model: "gpt-test", BaseURL: "https://example.test/v1"}); err != nil {
		t.Fatalf("openai-compatible provider: %v", err)
	}
}

func TestNewProviderExplainsWhereToConfigureMissingKey(t *testing.T) {
	_, err := newProvider(ProviderConfig{
		Type:  "deepseek",
		Model: "deepseek-v4-flash",
	})
	if err == nil {
		t.Fatal("newProvider() unexpectedly accepted an empty key")
	}
	for _, want := range []string{"provider.api_key", "~/.coding-agent/config.json", "--config"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not explain %q", err, want)
		}
	}
}
