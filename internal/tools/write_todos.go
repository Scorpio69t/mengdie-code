// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	maxTodos       = 64
	maxTodoIDBytes = 128
	maxTodoBytes   = 4 << 10
)

const writeTodosSchema = `{
  "type": "object",
  "properties": {
    "todos": {
      "type": "array",
      "maxItems": 64,
      "items": {
        "type": "object",
        "properties": {
          "id": {"type": "string"},
          "content": {"type": "string"},
          "status": {"type": "string", "enum": ["pending", "in_progress", "completed", "cancelled"]}
        },
        "required": ["id", "content", "status"],
        "additionalProperties": false
      }
    }
  },
  "required": ["todos"],
  "additionalProperties": false
}`

// TodoStatus is the bounded state machine vocabulary exposed to the model.
type TodoStatus string

const (
	TodoPending    TodoStatus = "pending"
	TodoInProgress TodoStatus = "in_progress"
	TodoCompleted  TodoStatus = "completed"
	TodoCancelled  TodoStatus = "cancelled"
)

// Todo is one run-scoped planning anchor. It is never written to the project.
type Todo struct {
	ID      string     `json:"id"`
	Content string     `json:"content"`
	Status  TodoStatus `json:"status"`
}

type writeTodosArgs struct {
	Todos []Todo `json:"todos"`
}

// TodoWriter is implemented by the Agent RunState. The tool package defines
// the narrow consumer-facing boundary and receives no access to other state.
type TodoWriter interface {
	ReplaceTodos(context.Context, []Todo) error
}

// NewWriteTodos builds the run-scoped planning tool.
func NewWriteTodos() Tool { return writeTodosTool{} }

type writeTodosTool struct{}

func (writeTodosTool) Spec() ToolSpec {
	return ToolSpec{
		Name:        "write_todos",
		Description: "替换当前任务计划；同一时间最多一项进行中，计划只存在本次运行内存中",
		InputSchema: json.RawMessage(writeTodosSchema),
		Effects:     []Effect{EffectState},
	}
}

func (writeTodosTool) Prepare(ctx context.Context, raw json.RawMessage, env PrepareEnv) (*PreparedCall, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var input writeTodosArgs
	if err := decodeArgs(raw, &input); err != nil {
		return nil, err
	}
	if err := validateTodos(input.Todos); err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("write_todos: encode prepared arguments: %w", err)
	}
	return PrepareCall(env.CallID, "write_todos", canonical,
		[]Effect{EffectState}, nil,
		Preview{Kind: PreviewNone, Title: "更新当前任务计划", Body: todoPreview(input.Todos)}, nil)
}

func (writeTodosTool) Execute(ctx context.Context, call *PreparedCall, cap Capability, env ExecEnv) (*ToolResult, error) {
	if err := CheckCapability(ctx, call, cap, env); err != nil {
		return nil, err
	}
	if env.TodoWriter == nil {
		return nil, errors.New("write_todos: run state writer is required")
	}
	var prepared writeTodosArgs
	if err := decodeArgs(call.CanonicalArg, &prepared); err != nil {
		return nil, err
	}
	if err := validateTodos(prepared.Todos); err != nil {
		return nil, err
	}
	if err := env.TodoWriter.ReplaceTodos(ctx, cloneTodos(prepared.Todos)); err != nil {
		return nil, fmt.Errorf("write_todos: update run state: %w", err)
	}
	return &ToolResult{
		Output: fmt.Sprintf("计划已更新，共 %d 项", len(prepared.Todos)),
		Metadata: map[string]string{
			"todo_count": fmt.Sprintf("%d", len(prepared.Todos)),
		},
	}, nil
}

func validateTodos(todos []Todo) error {
	if len(todos) > maxTodos {
		return fmt.Errorf("write_todos: at most %d todos are allowed", maxTodos)
	}
	seen := make(map[string]struct{}, len(todos))
	inProgress := 0
	for index, todo := range todos {
		if strings.TrimSpace(todo.ID) == "" || len(todo.ID) > maxTodoIDBytes {
			return fmt.Errorf("write_todos: todo %d has an invalid id", index)
		}
		if _, duplicate := seen[todo.ID]; duplicate {
			return fmt.Errorf("write_todos: duplicate todo id %q", todo.ID)
		}
		seen[todo.ID] = struct{}{}
		if strings.TrimSpace(todo.Content) == "" || len(todo.Content) > maxTodoBytes {
			return fmt.Errorf("write_todos: todo %q has invalid content", todo.ID)
		}
		switch todo.Status {
		case TodoPending, TodoCompleted, TodoCancelled:
		case TodoInProgress:
			inProgress++
		default:
			return fmt.Errorf("write_todos: todo %q has invalid status %q", todo.ID, todo.Status)
		}
	}
	if inProgress > 1 {
		return errors.New("write_todos: at most one todo may be in_progress")
	}
	return nil
}

func todoPreview(todos []Todo) string {
	if len(todos) == 0 {
		return "清空当前任务计划"
	}
	var preview strings.Builder
	for index, todo := range todos {
		fmt.Fprintf(&preview, "%d. [%s] %s", index+1, todo.Status, todo.Content)
		if index+1 < len(todos) {
			preview.WriteByte('\n')
		}
	}
	return preview.String()
}

func cloneTodos(todos []Todo) []Todo {
	return append([]Todo(nil), todos...)
}
