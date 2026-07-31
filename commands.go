package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

type CommandResult struct {
	Output string
	Exit   bool
}

type CommandFunc func(context.Context, []string) (CommandResult, error)

type Command struct {
	Name string
	Help string
	Run  CommandFunc
}

type CommandRegistry struct {
	commands map[string]Command
}

func NewCommandRegistry() *CommandRegistry {
	return &CommandRegistry{commands: make(map[string]Command)}
}

func (r *CommandRegistry) Register(command Command) error {
	if !strings.HasPrefix(command.Name, "/") || strings.ContainsAny(command.Name, " \t\n") {
		return fmt.Errorf("命令名 %q 必须以 / 开头且不能包含空白字符", command.Name)
	}
	if command.Run == nil {
		return fmt.Errorf("命令 %q 没有处理函数", command.Name)
	}
	if _, exists := r.commands[command.Name]; exists {
		return fmt.Errorf("命令 %q 已注册", command.Name)
	}
	r.commands[command.Name] = command
	return nil
}

func (r *CommandRegistry) TryRun(ctx context.Context, line string) (bool, CommandResult, error) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "/") {
		return false, CommandResult{}, nil
	}
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return false, CommandResult{}, nil
	}
	command, ok := r.commands[fields[0]]
	if !ok {
		return true, CommandResult{Output: fmt.Sprintf("未知命令 %s；输入 /help 查看可用命令。", fields[0])}, nil
	}
	result, err := command.Run(ctx, fields[1:])
	return true, result, err
}

func (r *CommandRegistry) Help() string {
	commands := make([]Command, 0, len(r.commands))
	for _, command := range r.commands {
		commands = append(commands, command)
	}
	sort.Slice(commands, func(i, j int) bool {
		return commands[i].Name < commands[j].Name
	})
	lines := make([]string, 0, len(commands))
	for _, command := range commands {
		lines = append(lines, fmt.Sprintf("%-10s %s", command.Name, command.Help))
	}
	return strings.Join(lines, "\n")
}
