package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Workspace struct {
	root string
}

func NewWorkspace(root string) (Workspace, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return Workspace{}, fmt.Errorf("解析 workspace 路径: %w", err)
	}
	realRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return Workspace{}, fmt.Errorf("解析 workspace %q: %w", root, err)
	}
	info, err := os.Stat(realRoot)
	if err != nil {
		return Workspace{}, fmt.Errorf("读取 workspace %q: %w", root, err)
	}
	if !info.IsDir() {
		return Workspace{}, fmt.Errorf("workspace %q 不是目录", root)
	}
	return Workspace{root: realRoot}, nil
}

func (w Workspace) Root() string {
	return w.root
}

func (w Workspace) ResolveRead(path string) (string, error) {
	candidate, err := w.resolveCandidate(path)
	if err != nil {
		return "", err
	}
	if isSecretFile(path) {
		return "", fmt.Errorf("拒绝读取可能包含密钥的文件 %q", path)
	}
	info, err := os.Lstat(candidate)
	if err != nil {
		return "", fmt.Errorf("读取文件 %q: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("拒绝通过符号链接读取 %q", path)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%q 是目录，不能按文件读取", path)
	}
	realPath, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("解析文件 %q: %w", path, err)
	}
	if !w.contains(realPath) {
		return "", fmt.Errorf("拒绝读取 workspace 外的路径 %q", path)
	}
	return realPath, nil
}

func isSecretFile(path string) bool {
	base := filepath.Base(filepath.Clean(path))
	base = strings.ToLower(base)
	return base == ".env" || strings.HasPrefix(base, ".env.")
}

func (w Workspace) ResolveWrite(path string) (string, error) {
	candidate, err := w.resolveCandidate(path)
	if err != nil {
		return "", err
	}

	realParent, err := filepath.EvalSymlinks(filepath.Dir(candidate))
	if err != nil {
		return "", fmt.Errorf("解析目标目录 %q: %w", path, err)
	}
	if !w.contains(realParent) {
		return "", fmt.Errorf("拒绝写入 workspace 外的路径 %q", path)
	}

	info, err := os.Lstat(candidate)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("拒绝通过符号链接写入 %q", path)
		}
		if info.IsDir() {
			return "", fmt.Errorf("%q 是目录，不能写入", path)
		}
		realPath, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			return "", fmt.Errorf("解析目标文件 %q: %w", path, err)
		}
		if !w.contains(realPath) {
			return "", fmt.Errorf("拒绝写入 workspace 外的路径 %q", path)
		}
		return realPath, nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("读取目标文件 %q: %w", path, err)
	}
	return filepath.Join(realParent, filepath.Base(candidate)), nil
}

func (w Workspace) resolveCandidate(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("文件路径不能为空")
	}
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("拒绝绝对路径 %q", path)
	}
	clean := filepath.Clean(path)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("拒绝越出 workspace 的路径 %q", path)
	}
	return filepath.Join(w.root, clean), nil
}

func (w Workspace) contains(target string) bool {
	relative, err := filepath.Rel(w.root, target)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func atomicWrite(path string, content []byte, mode os.FileMode) (returnErr error) {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".coding-agent-*.tmp")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer func() {
		if removeErr := os.Remove(temporaryName); removeErr != nil && !os.IsNotExist(removeErr) && returnErr == nil {
			returnErr = fmt.Errorf("清理临时文件: %w", removeErr)
		}
	}()
	if err := temporary.Chmod(mode.Perm()); err != nil {
		return closeTemporaryFile(temporary, err)
	}
	if _, err := temporary.Write(content); err != nil {
		return closeTemporaryFile(temporary, err)
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, path)
}
