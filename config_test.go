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

func TestProviderTypeInferredFromBaseURL(t *testing.T) {
	base := t.TempDir()

	t.Run("base_url 非空且未指定 type，推断为 openai-compatible", func(t *testing.T) {
		config, err := normalizeConfig(Config{
			Provider: ProviderConfig{BaseURL: "https://example.test/v1"},
		}, base)
		if err != nil {
			t.Fatalf("normalizeConfig(): %v", err)
		}
		if config.Provider.Type != "openai-compatible" {
			t.Fatalf("type = %q, want openai-compatible", config.Provider.Type)
		}
	})

	t.Run("base_url 为空且未指定 type，推断为 deepseek", func(t *testing.T) {
		config, err := normalizeConfig(Config{}, base)
		if err != nil {
			t.Fatalf("normalizeConfig(): %v", err)
		}
		if config.Provider.Type != "deepseek" {
			t.Fatalf("type = %q, want deepseek", config.Provider.Type)
		}
	})

	t.Run("显式指定 type 时优先于推断", func(t *testing.T) {
		config, err := normalizeConfig(Config{
			Provider: ProviderConfig{Type: "deepseek", BaseURL: "https://example.test/v1"},
		}, base)
		if err != nil {
			t.Fatalf("normalizeConfig(): %v", err)
		}
		if config.Provider.Type != "deepseek" {
			t.Fatalf("type = %q, want deepseek（显式指定优先）", config.Provider.Type)
		}
	})
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
