// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package terminal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/events"
)

func TestJSONRendererWritesOneEventPerLine(t *testing.T) {
	var output bytes.Buffer
	renderer, err := NewJSONRenderer(&output)
	if err != nil {
		t.Fatal(err)
	}
	event := testEvent(t, 1, events.KindRunStarted, events.RunStarted{Model: "deepseek:chat"})
	if err := renderer.Emit(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("lines = %d: %q", len(lines), output.String())
	}
	var decoded events.Event
	if err := json.Unmarshal([]byte(lines[0]), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.RunID != "run-test" || decoded.Kind != events.KindRunStarted || decoded.Seq != 1 {
		t.Fatalf("decoded = %+v", decoded)
	}
}

func TestHumanRendererRendersKnownEvents(t *testing.T) {
	var output bytes.Buffer
	renderer, err := NewHumanRenderer(&output)
	if err != nil {
		t.Fatal(err)
	}
	inputs := []events.Event{
		testEvent(t, 1, events.KindRunStarted, events.RunStarted{Model: "deepseek:chat"}),
		testEvent(t, 2, events.KindTodoUpdated, events.TodoUpdated{Todos: []events.Todo{{Content: "读取失败测试", Status: "in_progress"}}}),
		testEvent(t, 3, events.KindToolCompleted, events.ToolCompleted{Tool: "go test", Success: true, Summary: "通过"}),
		testEvent(t, 4, events.KindRunCompleted, events.RunCompleted{Summary: "验证完成"}),
	}
	for _, event := range inputs {
		if err := renderer.Emit(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []string{"开始任务", "模型：deepseek:chat", "[进行中] 读取失败测试", "✓ go test：通过", "✓ 任务完成：验证完成"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("output does not contain %q: %s", want, output.String())
		}
	}
}

func TestHumanRendererDoesNotDumpUnknownPayload(t *testing.T) {
	var output bytes.Buffer
	renderer, err := NewHumanRenderer(&output)
	if err != nil {
		t.Fatal(err)
	}
	event := testEvent(t, 1, events.Kind("future.event"), map[string]string{"secret": "do-not-print"})
	if err := renderer.Emit(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "do-not-print") || output.String() != "• future.event\n" {
		t.Fatalf("output = %q", output.String())
	}
}

func TestHumanRendererDoesNotDuplicateCompletedStream(t *testing.T) {
	var output bytes.Buffer
	renderer, err := NewHumanRenderer(&output)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []events.Event{
		testEvent(t, 1, events.KindMessageDelta, events.MessageDelta{Text: "修复"}),
		testEvent(t, 2, events.KindMessageDelta, events.MessageDelta{Text: "完成"}),
		testEvent(t, 3, events.KindMessageCompleted, events.MessageCompleted{Text: "修复完成"}),
	} {
		if err := renderer.Emit(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	if output.String() != "修复完成\n" {
		t.Fatalf("output=%q", output.String())
	}
}

func TestRenderersPropagateValidationContextAndWriterErrors(t *testing.T) {
	jsonRenderer, err := NewJSONRenderer(errorWriter{})
	if err != nil {
		t.Fatal(err)
	}
	if err := jsonRenderer.Emit(context.Background(), testEvent(t, 1, events.KindWarning, events.Warning{Message: "test"})); !errors.Is(err, errWrite) {
		t.Fatalf("JSON Emit() error = %v", err)
	}
	humanRenderer, err := NewHumanRenderer(errorWriter{})
	if err != nil {
		t.Fatal(err)
	}
	if err := humanRenderer.Emit(context.Background(), testEvent(t, 1, events.KindWarning, events.Warning{Message: "test"})); !errors.Is(err, errWrite) {
		t.Fatalf("Human Emit() error = %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := humanRenderer.Emit(cancelled, testEvent(t, 2, events.KindWarning, events.Warning{Message: "test"})); !errors.Is(err, context.Canceled) {
		t.Fatalf("Human Emit() error = %v", err)
	}
	invalid := testEvent(t, 3, events.KindWarning, events.Warning{Message: "test"})
	invalid.Version = 2
	if err := humanRenderer.Emit(context.Background(), invalid); err == nil {
		t.Fatal("Human Emit() accepted unsupported version")
	}
}

func testEvent(t *testing.T, seq uint64, kind events.Kind, payload any) events.Event {
	t.Helper()
	event, err := events.New("run-test", seq, time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC), kind, payload)
	if err != nil {
		t.Fatal(err)
	}
	return event
}

var errWrite = errors.New("write failed")

type errorWriter struct{}

func (errorWriter) Write([]byte) (int, error) { return 0, errWrite }
