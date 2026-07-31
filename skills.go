package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/arixbit/gofer/agent"
)

const maxSkillContentBytes = 64 * 1024

// skillRecord keeps only the metadata discovered at startup. The full SKILL.md
// body remains on disk until the model explicitly calls load_skill.
type skillRecord struct {
	name        string
	description string
	path        string
}

// skillLoader exposes the configured external skills through one lazy-loading
// tool instead of placing every skill body in every model request.
type skillLoader struct {
	skills map[string]skillRecord
	names  []string
}

// newSkillLoader scans every configured root. A root may itself contain a
// SKILL.md, or it may contain skill directories one level below it.
func newSkillLoader(roots []string) (*skillLoader, error) {
	if len(roots) == 0 {
		return nil, nil
	}

	loader := &skillLoader{skills: make(map[string]skillRecord)}
	for _, root := range roots {
		paths, err := skillFilesInRoot(root)
		if err != nil {
			return nil, err
		}
		for _, path := range paths {
			name, description, err := readSkillMetadata(path)
			if err != nil {
				return nil, fmt.Errorf("解析 Skill %q: %w", path, err)
			}
			if existing, exists := loader.skills[name]; exists {
				return nil, fmt.Errorf("技能名称 %q 重复：%q 与 %q", name, existing.path, path)
			}
			loader.skills[name] = skillRecord{
				name:        name,
				description: description,
				path:        path,
			}
			loader.names = append(loader.names, name)
		}
	}
	if len(loader.names) == 0 {
		return nil, fmt.Errorf("配置的 Skill 根目录中没有找到 SKILL.md")
	}
	sort.Strings(loader.names)
	return loader, nil
}

func skillFilesInRoot(root string) ([]string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("读取 Skill 根目录 %q: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("技能根目录 %q 不是目录", root)
	}

	paths := make([]string, 0)
	rootSkill := filepath.Join(root, "SKILL.md")
	if info, err := os.Stat(rootSkill); err == nil {
		if info.IsDir() {
			return nil, fmt.Errorf("技能文件 %q 是目录", rootSkill)
		}
		paths = append(paths, rootSkill)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("检查 Skill 文件 %q: %w", rootSkill, err)
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("读取 Skill 根目录 %q: %w", root, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(root, entry.Name(), "SKILL.md")
		if info, err := os.Stat(path); err == nil {
			if info.IsDir() {
				return nil, fmt.Errorf("技能文件 %q 是目录", path)
			}
			paths = append(paths, path)
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("检查 Skill 文件 %q: %w", path, err)
		}
	}
	return paths, nil
}

func readSkillMetadata(path string) (name, description string, returnErr error) {
	file, err := os.Open(path)
	if err != nil {
		return "", "", fmt.Errorf("打开 Skill 文件: %w", err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil && returnErr == nil {
			name = ""
			description = ""
			returnErr = fmt.Errorf("关闭 Skill 文件 %q: %w", path, closeErr)
		}
	}()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), 1024*1024)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", "", fmt.Errorf("读取 YAML frontmatter: %w", err)
		}
		return "", "", fmt.Errorf("缺少 YAML frontmatter")
	}
	if strings.TrimPrefix(strings.TrimSpace(scanner.Text()), "\ufeff") != "---" {
		return "", "", fmt.Errorf("缺少 YAML frontmatter")
	}

	closed := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "---" {
			closed = true
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		switch strings.TrimSpace(key) {
		case "name":
			name = value
		case "description":
			description = value
		}
	}
	if err := scanner.Err(); err != nil {
		return "", "", fmt.Errorf("读取 YAML frontmatter: %w", err)
	}
	if !closed {
		return "", "", fmt.Errorf("YAML frontmatter 没有结束标记")
	}
	if name == "" || description == "" {
		return "", "", fmt.Errorf("frontmatter 必须包含 name 和 description")
	}
	return name, description, nil
}

func (t *skillLoader) Definition() agent.ToolDefinition {
	descriptions := make([]string, 0, len(t.names))
	for _, name := range t.names {
		record := t.skills[name]
		descriptions = append(descriptions, fmt.Sprintf("%s: %s", record.name, record.description))
	}
	return agent.ToolDefinition{
		Name: "load_skill",
		Description: "按名称加载一份 Skill 的完整工作说明。不要自动加载；只有任务确实需要时才调用。可用 Skill：" +
			strings.Join(descriptions, "；"),
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "要加载的 Skill 名称",
					"enum":        t.names,
				},
			},
			"required":             []string{"name"},
			"additionalProperties": false,
		},
	}
}

func (t *skillLoader) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var params struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return "", fmt.Errorf("解析 load_skill 参数: %w", err)
	}
	record, ok := t.skills[params.Name]
	if !ok {
		return "", fmt.Errorf("未知 Skill: %s", params.Name)
	}
	content, err := readBoundedSkillFile(record.path)
	if err != nil {
		return "", fmt.Errorf("读取 Skill %q: %w", record.path, err)
	}
	return string(content), nil
}

func readBoundedSkillFile(path string) (content []byte, returnErr error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("不是普通文件")
	}
	if info.Size() > maxSkillContentBytes {
		return nil, fmt.Errorf("超过 %d 字节上限", maxSkillContentBytes)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil && returnErr == nil {
			content = nil
			returnErr = fmt.Errorf("关闭 Skill 文件 %q: %w", path, closeErr)
		}
	}()
	content, err = io.ReadAll(io.LimitReader(file, maxSkillContentBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) > maxSkillContentBytes {
		return nil, fmt.Errorf("超过 %d 字节上限", maxSkillContentBytes)
	}
	return content, nil
}
