package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStartupConfigUsesInvocationDirectoryByDefault(t *testing.T) {
	project := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	config, err := startupConfig(nil, project)
	if err != nil {
		t.Fatalf("startupConfig() error = %v", err)
	}
	if config.Workspace != project {
		t.Fatalf("workspace = %q, want %q", config.Workspace, project)
	}
	wantSessions := filepath.Join(project, ".coding-agent", "sessions")
	if config.SessionDir != wantSessions {
		t.Fatalf("session dir = %q, want %q", config.SessionDir, wantSessions)
	}
	if config.Provider.Type != "deepseek" || config.Provider.APIKey != "" || config.Provider.Model != "deepseek-v4-flash" {
		t.Fatalf("default provider = %+v", config.Provider)
	}
}

func TestStartupConfigOverlaysExplicitConfigOnInvocationDirectory(t *testing.T) {
	project := t.TempDir()
	configFile := filepath.Join(t.TempDir(), "agent.json")
	if err := os.WriteFile(configFile, []byte(`{"provider":{"type":"deepseek","api_key":"test-key"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	config, err := startupConfig([]string{"--config", configFile}, project)
	if err != nil {
		t.Fatalf("startupConfig() error = %v", err)
	}
	if config.Workspace != project {
		t.Fatalf("explicit workspace = %q, want invocation directory %q", config.Workspace, project)
	}
	if config.Provider.APIKey != "test-key" {
		t.Fatalf("provider api key = %q, want test-key", config.Provider.APIKey)
	}
}

func TestStartupConfigAutoLoadsUserConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".coding-agent"), 0700); err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(home, ".coding-agent", "config.json")
	if err := os.WriteFile(configFile, []byte(`{"provider":{"api_key":"user-key"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	config, err := startupConfig(nil, project)
	if err != nil {
		t.Fatalf("startupConfig() error = %v", err)
	}
	if config.Workspace != project || config.Provider.APIKey != "user-key" {
		t.Fatalf("config = %+v, want workspace %q and user key", config, project)
	}
}
