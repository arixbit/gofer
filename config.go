package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/arixbit/gofer/agent"
)

type Config struct {
	Workspace    string            `json:"workspace"`
	SessionDir   string            `json:"session_dir"`
	Provider     ProviderConfig    `json:"provider"`
	Skills       []string          `json:"skills"`
	MCPServers   []MCPServerConfig `json:"mcp_servers"`
	SystemPrompt string            `json:"system_prompt"`
}

type ProviderConfig struct {
	Type    string `json:"type"`
	APIKey  string `json:"api_key"`
	Model   string `json:"model"`
	BaseURL string `json:"base_url"`
}

type MCPServerConfig struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type providerFactory func(ProviderConfig) (agent.ModelProvider, error)

// NewProjectConfig creates the zero-configuration path used by the CLI: the
// directory where the user invokes the Agent is the project it may operate on.
func NewProjectConfig(workspace string) (Config, error) {
	root, err := filepath.Abs(workspace)
	if err != nil {
		return Config{}, fmt.Errorf("解析项目目录: %w", err)
	}
	config := Config{
		Workspace:  root,
		SessionDir: filepath.Join(root, ".coding-agent", "sessions"),
	}
	applyProviderDefaults(&config)
	return config, nil
}

// LoadConfig reads a user-owned JSON file and overlays it on the defaults for
// the directory where the Agent was launched. An omitted workspace therefore
// keeps the current project instead of silently switching to the config file's
// directory.
func LoadConfig(filename, workingDir string) (result Config, resultErr error) {
	file, err := os.Open(filename)
	if err != nil {
		return Config{}, fmt.Errorf("打开配置文件 %q: %w", filename, err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil && resultErr == nil {
			result = Config{}
			resultErr = fmt.Errorf("关闭配置文件 %q: %w", filename, closeErr)
		}
	}()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var config Config
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("解析配置文件 %q: %w", filename, err)
	}

	defaults, err := NewProjectConfig(workingDir)
	if err != nil {
		return Config{}, err
	}
	configDir, err := filepath.Abs(filepath.Dir(filename))
	if err != nil {
		return Config{}, fmt.Errorf("解析配置文件目录: %w", err)
	}
	return mergeConfig(defaults, config, configDir), nil
}

func normalizeConfig(config Config, baseDir string) (Config, error) {
	defaults, err := NewProjectConfig(baseDir)
	if err != nil {
		return Config{}, err
	}
	return mergeConfig(defaults, config, baseDir), nil
}

func mergeConfig(defaults, override Config, configDir string) Config {
	merged := defaults
	if override.Workspace != "" {
		merged.Workspace = resolveConfigPath(configDir, override.Workspace)
	}
	if override.SessionDir != "" {
		merged.SessionDir = resolveConfigPath(configDir, override.SessionDir)
	} else if override.Workspace != "" {
		// When a config selects another workspace, keep its sessions beside it
		// unless the user explicitly selected a different session directory.
		merged.SessionDir = filepath.Join(merged.Workspace, ".coding-agent", "sessions")
	}
	if override.Provider.Type != "" {
		merged.Provider.Type = override.Provider.Type
	}
	if override.Provider.APIKey != "" {
		merged.Provider.APIKey = override.Provider.APIKey
	}
	if override.Provider.Model != "" {
		merged.Provider.Model = override.Provider.Model
	}
	if override.Provider.BaseURL != "" {
		merged.Provider.BaseURL = override.Provider.BaseURL
	}
	if override.Skills != nil {
		merged.Skills = make([]string, len(override.Skills))
		for i, root := range override.Skills {
			merged.Skills[i] = resolveConfigPath(configDir, root)
		}
	}
	if override.MCPServers != nil {
		merged.MCPServers = append([]MCPServerConfig(nil), override.MCPServers...)
	}
	if override.SystemPrompt != "" {
		merged.SystemPrompt = override.SystemPrompt
	}
	applyProviderDefaults(&merged)
	return merged
}

func applyProviderDefaults(config *Config) {
	config.Provider.Type = strings.ToLower(strings.TrimSpace(config.Provider.Type))
	if config.Provider.Type == "" {
		config.Provider.Type = "deepseek"
	}
	if config.Provider.Model == "" && config.Provider.Type == "deepseek" {
		config.Provider.Model = "deepseek-v4-flash"
	}
}

func resolveConfigPath(baseDir, value string) string {
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Join(baseDir, value)
}

func newProvider(config ProviderConfig) (agent.ModelProvider, error) {
	apiKey := strings.TrimSpace(config.APIKey)
	if apiKey == "" {
		return nil, fmt.Errorf("provider.api_key 未配置：请在 ~/.coding-agent/config.json 或 --config 指定的 JSON 中填写")
	}

	switch config.Type {
	case "deepseek":
		return agent.NewDeepSeekProviderWithModel(apiKey, config.Model), nil
	case "openai-compatible":
		if strings.TrimSpace(config.BaseURL) == "" {
			return nil, fmt.Errorf("openai-compatible provider 必须配置 base_url")
		}
		if strings.TrimSpace(config.Model) == "" {
			return nil, fmt.Errorf("openai-compatible provider 必须配置 model")
		}
		return agent.NewOpenAICompatibleProvider(apiKey, config.BaseURL, config.Model), nil
	default:
		return nil, fmt.Errorf("不支持的 provider 类型 %q（当前只支持 deepseek 和 openai-compatible）", config.Type)
	}
}
