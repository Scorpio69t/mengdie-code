// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type recordingTodoWriter struct{ todos []Todo }

func (writer *recordingTodoWriter) ReplaceTodos(_ context.Context, todos []Todo) error {
	writer.todos = cloneTodos(todos)
	return nil
}

func TestWriteTodosUpdatesOnlyRunState(t *testing.T) {
	env := newToolTestEnv(t)
	tool := NewWriteTodos()
	call := prepareCall(t, tool, env, mustJSON(t, writeTodosArgs{Todos: []Todo{
		{ID: "read", Content: "读取失败测试", Status: TodoCompleted},
		{ID: "fix", Content: "修复实现", Status: TodoInProgress},
	}}))
	writer := &recordingTodoWriter{}
	execEnv := env.execEnv()
	execEnv.TodoWriter = writer
	result, err := tool.Execute(context.Background(), call, capabilityFor(call), execEnv)
	if err != nil {
		t.Fatal(err)
	}
	if len(writer.todos) != 2 || writer.todos[1].Status != TodoInProgress || result.Metadata["todo_count"] != "2" {
		t.Fatalf("todos=%+v result=%+v", writer.todos, result)
	}
}

func TestWriteTodosRejectsInvalidPlans(t *testing.T) {
	env := newToolTestEnv(t)
	for name, todos := range map[string][]Todo{
		"duplicate":  {{ID: "same", Content: "one", Status: TodoPending}, {ID: "same", Content: "two", Status: TodoPending}},
		"two active": {{ID: "one", Content: "one", Status: TodoInProgress}, {ID: "two", Content: "two", Status: TodoInProgress}},
		"unknown":    {{ID: "one", Content: "one", Status: "blocked"}},
		"empty":      {{ID: "one", Content: " ", Status: TodoPending}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewWriteTodos().Prepare(context.Background(), mustRawJSON(t, writeTodosArgs{Todos: todos}), env.prepareEnv())
			if err == nil {
				t.Fatal("Prepare() succeeded")
			}
		})
	}
}

func TestWriteTodosRequiresCapabilityAndWriter(t *testing.T) {
	env := newToolTestEnv(t)
	tool := NewWriteTodos()
	call := prepareCall(t, tool, env, mustJSON(t, writeTodosArgs{}))
	if _, err := tool.Execute(context.Background(), call, Capability{}, env.execEnv()); !errors.Is(err, ErrCapabilityMissing) {
		t.Fatalf("Execute() error = %v", err)
	}
	if _, err := tool.Execute(context.Background(), call, capabilityFor(call), env.execEnv()); err == nil || !strings.Contains(err.Error(), "writer") {
		t.Fatalf("Execute() error = %v", err)
	}
}
