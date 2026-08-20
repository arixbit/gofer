package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/arixbit/gofer/agent"
)

const sessionVersion = 3

type SessionStore interface {
	Save(SessionState) error
}

type SessionCatalog interface {
	NewSession() (SessionState, error)
	ListSessions() ([]SessionSummary, error)
	ResumeSession(id string) (SessionState, error)
	CurrentSessionID() string
}

// SessionState 同时保存给下一轮模型使用的 context，以及不因上下文压缩而丢失的 transcript。
type SessionState struct {
	Context    []agent.Message
	Transcript []agent.Message
}

type SessionSummary struct {
	ID        string
	CWD       string
	CreatedAt time.Time
	UpdatedAt time.Time
	Current   bool
	path      string
}

type FileSessionStore struct {
	dir         string
	cwd         string
	currentPath string
	currentID   string
}

type sessionHeader struct {
	Type      string    `json:"type"`
	Version   int       `json:"version"`
	ID        string    `json:"id"`
	CWD       string    `json:"cwd"`
	CreatedAt time.Time `json:"created_at"`
}

type sessionEntry struct {
	Type       string          `json:"type"`
	ID         string          `json:"id"`
	Timestamp  time.Time       `json:"timestamp"`
	Context    []storedMessage `json:"context,omitempty"`
	Transcript []storedMessage `json:"transcript,omitempty"`
}

type storedMessage struct {
	Role    string        `json:"role"`
	Content []storedBlock `json:"content"`
}

type storedBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	Text      string          `json:"text,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	Reasoning string          `json:"reasoning,omitempty"`
}

func NewFileSessionStore(dir, cwd string) *FileSessionStore {
	return &FileSessionStore{dir: dir, cwd: cwd}
}

func (s *FileSessionStore) Load() (SessionState, error) {
	if s.currentPath == "" {
		sessions, err := s.ListSessions()
		if err != nil {
			return SessionState{}, err
		}
		if len(sessions) == 0 {
			return SessionState{}, nil
		}
		s.currentPath = sessions[0].path
		s.currentID = sessions[0].ID
	}
	return loadSessionFile(s.currentPath)
}

func (s *FileSessionStore) Save(state SessionState) error {
	if s.currentPath == "" {
		if _, err := s.NewSession(); err != nil {
			return err
		}
	}
	contextMessages, err := storeMessages(state.Context)
	if err != nil {
		return err
	}
	transcript, err := storeMessages(state.Transcript)
	if err != nil {
		return err
	}
	entryID, err := newSessionID()
	if err != nil {
		return fmt.Errorf("生成会话记录 ID: %w", err)
	}
	entry := sessionEntry{
		Type:       "turn",
		ID:         entryID,
		Timestamp:  time.Now().UTC(),
		Context:    contextMessages,
		Transcript: transcript,
	}
	return appendJSONLine(s.currentPath, entry)
}

func (s *FileSessionStore) NewSession() (SessionState, error) {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return SessionState{}, fmt.Errorf("创建会话目录: %w", err)
	}
	id, err := newSessionID()
	if err != nil {
		return SessionState{}, fmt.Errorf("生成会话 ID: %w", err)
	}
	now := time.Now().UTC()
	filename := now.Format("20060102-150405") + "-" + id + ".jsonl"
	path := filepath.Join(s.dir, filename)
	header := sessionHeader{
		Type:      "session",
		Version:   sessionVersion,
		ID:        id,
		CWD:       s.cwd,
		CreatedAt: now,
	}
	if err := createJSONLine(path, header); err != nil {
		return SessionState{}, fmt.Errorf("创建会话文件: %w", err)
	}
	s.currentPath = path
	s.currentID = id
	return SessionState{}, nil
}

func (s *FileSessionStore) ListSessions() ([]SessionSummary, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取会话目录: %w", err)
	}

	sessions := make([]SessionSummary, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
			continue
		}
		path := filepath.Join(s.dir, entry.Name())
		header, err := readSessionHeader(path)
		if err != nil {
			return nil, fmt.Errorf("读取会话 %q: %w", entry.Name(), err)
		}
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("读取会话文件信息 %q: %w", entry.Name(), err)
		}
		sessions = append(sessions, SessionSummary{
			ID:        header.ID,
			CWD:       header.CWD,
			CreatedAt: header.CreatedAt,
			UpdatedAt: info.ModTime(),
			Current:   path == s.currentPath,
			path:      path,
		})
	}
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].CreatedAt.After(sessions[j].CreatedAt)
	})
	return sessions, nil
}

func (s *FileSessionStore) ResumeSession(id string) (SessionState, error) {
	sessions, err := s.ListSessions()
	if err != nil {
		return SessionState{}, err
	}
	for _, session := range sessions {
		if session.ID != id {
			continue
		}
		state, err := loadSessionFile(session.path)
		if err != nil {
			return SessionState{}, err
		}
		s.currentPath = session.path
		s.currentID = session.ID
		return state, nil
	}
	return SessionState{}, fmt.Errorf("没有找到会话 %q", id)
}

func (s *FileSessionStore) CurrentSessionID() string {
	return s.currentID
}

func readSessionHeader(path string) (sessionHeader, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return sessionHeader{}, err
	}
	firstLine := strings.SplitN(string(content), "\n", 2)[0]
	var header sessionHeader
	if err := json.Unmarshal([]byte(firstLine), &header); err != nil {
		return sessionHeader{}, fmt.Errorf("解析会话头: %w", err)
	}
	if header.Type != "session" || header.Version != sessionVersion || header.ID == "" {
		return sessionHeader{}, fmt.Errorf("会话头无效")
	}
	return header, nil
}

func loadSessionFile(path string) (returnState SessionState, returnErr error) {
	file, err := os.Open(path)
	if err != nil {
		return SessionState{}, fmt.Errorf("打开会话文件 %q: %w", path, err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil && returnErr == nil {
			returnState = SessionState{}
			returnErr = fmt.Errorf("关闭会话文件 %q: %w", path, closeErr)
		}
	}()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), 16*1024*1024)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return SessionState{}, fmt.Errorf("读取会话头: %w", err)
		}
		return SessionState{}, fmt.Errorf("会话文件为空")
	}
	var header sessionHeader
	if err := json.Unmarshal(scanner.Bytes(), &header); err != nil {
		return SessionState{}, fmt.Errorf("解析会话头: %w", err)
	}
	if header.Type != "session" || header.Version != sessionVersion {
		return SessionState{}, fmt.Errorf("会话头版本不支持")
	}

	var state SessionState
	for scanner.Scan() {
		var entry sessionEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			return SessionState{}, fmt.Errorf("解析会话记录: %w", err)
		}
		if entry.Type != "turn" {
			return SessionState{}, fmt.Errorf("不支持的会话记录类型 %q", entry.Type)
		}
		contextMessages, err := restoreMessages(entry.Context)
		if err != nil {
			return SessionState{}, err
		}
		transcript, err := restoreMessages(entry.Transcript)
		if err != nil {
			return SessionState{}, err
		}
		state = SessionState{Context: contextMessages, Transcript: transcript}
	}
	if err := scanner.Err(); err != nil {
		return SessionState{}, fmt.Errorf("读取会话记录: %w", err)
	}
	return state, nil
}

func createJSONLine(path string, value any) error {
	return writeJSONLine(path, value, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
}

func appendJSONLine(path string, value any) error {
	return writeJSONLine(path, value, os.O_WRONLY|os.O_CREATE|os.O_APPEND)
}

func writeJSONLine(path string, value any, flags int) (returnErr error) {
	content, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("编码会话记录: %w", err)
	}
	file, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil && returnErr == nil {
			returnErr = fmt.Errorf("关闭会话文件: %w", closeErr)
		}
	}()
	if _, err := file.Write(append(content, '\n')); err != nil {
		return fmt.Errorf("写入会话文件: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("同步会话文件: %w", err)
	}
	return nil
}

func closeTemporaryFile(file *os.File, cause error) error {
	if closeErr := file.Close(); closeErr != nil {
		return fmt.Errorf("%w；关闭临时文件: %v", cause, closeErr)
	}
	return cause
}

func newSessionID() (string, error) {
	var raw [4]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func storeMessages(messages []agent.Message) ([]storedMessage, error) {
	stored := make([]storedMessage, 0, len(messages))
	for _, message := range messages {
		entry := storedMessage{Role: message.Role, Content: make([]storedBlock, 0, len(message.Content))}
		for _, block := range message.Content {
			storedBlock, err := storeBlock(block)
			if err != nil {
				return nil, err
			}
			entry.Content = append(entry.Content, storedBlock)
		}
		stored = append(stored, entry)
	}
	return stored, nil
}

func storeBlock(block agent.ContentBlock) (storedBlock, error) {
	switch block.Type() {
	case "text":
		return storedBlock{Type: "text", Text: block.Text()}, nil
	case "tool_use":
		return storedBlock{
			Type:  "tool_use",
			ID:    block.ID(),
			Name:  block.Name(),
			Input: append(json.RawMessage(nil), block.Input()...),
		}, nil
	case "tool_result":
		return storedBlock{
			Type:    "tool_result",
			ID:      block.ID(),
			Text:    block.Text(),
			IsError: block.IsError(),
		}, nil
	case "reasoning":
		return storedBlock{
			Type:      "reasoning",
			Reasoning: block.Reasoning(),
		}, nil
	default:
		return storedBlock{}, fmt.Errorf("不支持保存的内容块类型 %q", block.Type())
	}
}

func restoreMessages(messages []storedMessage) ([]agent.Message, error) {
	restored := make([]agent.Message, 0, len(messages))
	for _, message := range messages {
		if message.Role != "user" && message.Role != "assistant" {
			return nil, fmt.Errorf("不支持的消息角色 %q", message.Role)
		}
		restoredMessage := agent.Message{Role: message.Role, Content: make([]agent.ContentBlock, 0, len(message.Content))}
		for _, block := range message.Content {
			restoredBlock, err := restoreBlock(block)
			if err != nil {
				return nil, err
			}
			restoredMessage.Content = append(restoredMessage.Content, restoredBlock)
		}
		restored = append(restored, restoredMessage)
	}
	return restored, nil
}

// restoreBlock 根据存储的 type tag 重建接口背后的具体 ContentBlock。
func restoreBlock(block storedBlock) (agent.ContentBlock, error) {
	switch block.Type {
	case "text":
		return agent.NewTextBlock(block.Text), nil
	case "tool_use":
		if block.ID == "" || block.Name == "" || len(block.Input) == 0 {
			return nil, fmt.Errorf("tool_use 内容块缺少 id、name 或 input")
		}
		// clone RawMessage：恢复后的 block 不与 JSON decoder 的缓冲区共享字节。
		return agent.NewToolUseBlock(block.ID, block.Name, append(json.RawMessage(nil), block.Input...)), nil
	case "tool_result":
		if block.ID == "" {
			return nil, fmt.Errorf("tool_result 内容块缺少 id")
		}
		return agent.NewToolResultBlock(block.ID, block.Text, block.IsError), nil
	case "reasoning":
		return agent.NewReasoningBlock(block.Reasoning), nil
	default:
		return nil, fmt.Errorf("不支持恢复的内容块类型 %q", block.Type)
	}
}
