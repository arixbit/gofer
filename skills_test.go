package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewSkillLoaderScansConfiguredRootsAndLoadsOnDemand(t *testing.T) {
	root := t.TempDir()
	rootSkill := filepath.Join(root, "SKILL.md")
	writeTestSkill(t, rootSkill, "root-skill", "根目录里的 Skill", "旧正文")
	writeTestSkill(t, filepath.Join(root, "nested", "SKILL.md"), "nested-skill", "一级子目录里的 Skill", "嵌套正文")

	loader, err := newSkillLoader([]string{root})
	if err != nil {
		t.Fatalf("newSkillLoader() error = %v", err)
	}
	definition := loader.Definition()
	if definition.Name != "load_skill" {
		t.Fatalf("tool name = %q, want load_skill", definition.Name)
	}
	for _, want := range []string{"root-skill: 根目录里的 Skill", "nested-skill: 一级子目录里的 Skill"} {
		if !strings.Contains(definition.Description, want) {
			t.Fatalf("definition description = %q, missing %q", definition.Description, want)
		}
	}

	// Replacing the body after discovery proves the loader reads the full file
	// only when the tool is called, rather than caching it in model context.
	writeTestSkill(t, rootSkill, "root-skill", "根目录里的 Skill", "新正文")
	got, err := loader.Execute(context.Background(), json.RawMessage(`{"name":"root-skill"}`))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(got, "新正文") || strings.Contains(got, "旧正文") {
		t.Fatalf("loaded content = %q", got)
	}
}

func TestNewSkillLoaderRejectsDuplicateNamesAcrossRoots(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	writeTestSkill(t, filepath.Join(first, "SKILL.md"), "same", "first", "body")
	writeTestSkill(t, filepath.Join(second, "SKILL.md"), "same", "second", "body")

	_, err := newSkillLoader([]string{first, second})
	if err == nil || !strings.Contains(err.Error(), "重复") {
		t.Fatalf("newSkillLoader() error = %v, want duplicate-name error", err)
	}
}

func TestSkillLoaderRejectsOversizedBody(t *testing.T) {
	root := t.TempDir()
	writeTestSkill(t, filepath.Join(root, "SKILL.md"), "large", "large skill", strings.Repeat("x", maxSkillContentBytes+1))
	loader, err := newSkillLoader([]string{root})
	if err != nil {
		t.Fatalf("newSkillLoader(): %v", err)
	}
	if _, err := loader.Execute(context.Background(), json.RawMessage(`{"name":"large"}`)); err == nil || !strings.Contains(err.Error(), "上限") {
		t.Fatalf("Execute() error = %v, want size-limit error", err)
	}
}

func writeTestSkill(t *testing.T, path, name, description, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
	}
	content := fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n\n%s\n", name, description, body)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}
