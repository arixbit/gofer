package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

func main() {
	workingDir, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}
	config, err := startupConfig(os.Args[1:], workingDir)
	if err != nil {
		log.Fatal(err)
	}

	input := bufio.NewReader(os.Stdin)
	application, closer, err := buildApplication(context.Background(), config, newProvider)
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := closer.Close(); err != nil {
			log.Printf("关闭 MCP 连接: %v", err)
		}
	}()
	if err := runREPL(context.Background(), application, input, os.Stdout); err != nil {
		log.Fatal(err)
	}
}

// startupConfig uses the invocation directory as the workspace and overlays a
// user-owned config file when one is available. An explicit --config path
// takes precedence over ~/.coding-agent/config.json.
func startupConfig(args []string, workingDir string) (Config, error) {
	defaults, err := NewProjectConfig(workingDir)
	if err != nil {
		return Config{}, err
	}
	configPath, err := resolveConfigArgument(args)
	if err != nil {
		return Config{}, err
	}
	if configPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Config{}, fmt.Errorf("解析用户目录: %w", err)
		}
		candidate := filepath.Join(home, ".coding-agent", "config.json")
		if _, err := os.Stat(candidate); err != nil {
			if os.IsNotExist(err) {
				return defaults, nil
			}
			return Config{}, fmt.Errorf("检查用户配置 %q: %w", candidate, err)
		}
		configPath = candidate
	}
	return LoadConfig(configPath, workingDir)
}

func resolveConfigArgument(args []string) (string, error) {
	switch len(args) {
	case 0:
		return "", nil
	case 1:
		// Keep the short positional form for the teaching version.
		if args[0] == "--config" {
			return "", fmt.Errorf("--config 需要一个 JSON 文件路径")
		}
		return args[0], nil
	case 2:
		if args[0] != "--config" {
			return "", fmt.Errorf("用法: %s [--config config.json]", os.Args[0])
		}
		return args[1], nil
	default:
		return "", fmt.Errorf("用法: %s [--config config.json]", os.Args[0])
	}
}
