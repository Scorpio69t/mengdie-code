// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/Scorpio69t/mengdie-code/internal/brand"
	"github.com/Scorpio69t/mengdie-code/internal/policy"
	"github.com/Scorpio69t/mengdie-code/internal/tools"
)

func TestInteractiveModelWelcomeIsChineseFirstResponsiveAndColorFree(t *testing.T) {
	info := interactiveTestInfo()
	model := NewInteractiveModel(info, &fakeTaskRunner{}, nil, nil, false)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 36, Height: 22})
	model = updated.(InteractiveModel)
	content := model.View().Content
	for _, want := range []string{"MengDie Code / 梦蝶 Code", "不是记得更多", info.WorkDir, info.Model, info.Security, "请描述", "Ctrl+S"} {
		if !strings.Contains(content, want) {
			t.Errorf("welcome missing %q:\n%s", want, content)
		}
	}
	if strings.Contains(content, "\x1b[") {
		t.Fatalf("no-color view contains ANSI sequence: %q", content)
	}
}

func TestInteractiveModelSubmitsBoundedTaskAndCancelsSafely(t *testing.T) {
	runner := &fakeTaskRunner{execution: &fakeTaskExecution{sessionID: "ses-test"}}
	model := NewInteractiveModel(interactiveTestInfo(), runner, nil, nil, false)
	model.input.SetValue("  检查项目  ")

	updated, command := model.Update(tea.KeyPressMsg(tea.Key{Code: 's', Mod: tea.ModCtrl}))
	model = updated.(InteractiveModel)
	if command == nil || model.phase != phaseStarting {
		t.Fatalf("submit command=%v phase=%v", command, model.phase)
	}
	prepared := command()
	updated, _ = model.Update(prepared)
	model = updated.(InteractiveModel)
	if runner.task != "检查项目" || model.phase != phaseRunning || model.view.ID != "ses-test" {
		t.Fatalf("task=%q phase=%v view=%+v", runner.task, model.phase, model.view)
	}

	updated, quit := model.Update(tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl}))
	model = updated.(InteractiveModel)
	if quit != nil || !runner.execution.cancelled || model.phase != phaseCancelling {
		t.Fatalf("quit=%v cancelled=%t phase=%v", quit, runner.execution.cancelled, model.phase)
	}
	if !strings.Contains(model.View().Content, "等待持久终态") {
		t.Fatalf("cancelling view does not explain durable shutdown: %s", model.View().Content)
	}
}

func TestInteractiveModelSupportsPortableSubmitKeys(t *testing.T) {
	keys := []tea.Key{
		{Code: 's', Mod: tea.ModCtrl},
		{Code: tea.KeyEnter, Mod: tea.ModCtrl},
	}
	for _, key := range keys {
		t.Run(key.String(), func(t *testing.T) {
			runner := &fakeTaskRunner{}
			model := NewInteractiveModel(interactiveTestInfo(), runner, nil, nil, false)
			model.input.SetValue("检查项目")
			updated, command := model.Update(tea.KeyPressMsg(key))
			model = updated.(InteractiveModel)
			if command == nil || model.phase != phaseStarting {
				t.Fatalf("key=%s command=%v phase=%v", key.String(), command, model.phase)
			}
		})
	}
}

func TestInteractiveModelRejectsTaskOverByteLimit(t *testing.T) {
	model := NewInteractiveModel(interactiveTestInfo(), &fakeTaskRunner{}, nil, nil, false)
	model.input.SetValue(strings.Repeat("蝶", MaxInteractiveTaskBytes/3+1))
	updated, command := model.Update(tea.KeyPressMsg(tea.Key{Code: 's', Mod: tea.ModCtrl}))
	model = updated.(InteractiveModel)
	if command != nil || !strings.Contains(model.inputError, "64 KiB") {
		t.Fatalf("command=%v inputError=%q", command, model.inputError)
	}
}

func TestInteractiveModelResolvesApprovalWithoutIssuingCapability(t *testing.T) {
	broker := NewApprovalBroker()
	defer broker.Close()
	model := NewInteractiveModel(interactiveTestInfo(), &fakeTaskRunner{}, nil, broker, false)
	result := make(chan policy.ApprovalResponse, 1)
	go func() {
		response, _ := broker.Decide(context.Background(), policy.ApprovalRequest{
			CallID: "call-edit", Tool: "edit_file", Risk: "medium",
			Preview: tools.Preview{Kind: tools.PreviewDiff, Title: "修改 main.go", Body: "- old\n+ new"},
		})
		result <- response
	}()
	prompt := receiveApprovalPrompt(t, broker.Prompts())
	updated, _ := model.Update(approvalPromptMsg{prompt: prompt})
	model = updated.(InteractiveModel)
	if !strings.Contains(model.View().Content, "需要审批") || !strings.Contains(model.View().Content, "修改 main.go") {
		t.Fatalf("approval preview missing: %s", model.View().Content)
	}
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Text: "y", Code: 'y'}))
	model = updated.(InteractiveModel)
	if model.approval != nil {
		t.Fatal("approval remains visible after decision")
	}
	if response := <-result; response.Choice != policy.ApprovalApprove {
		t.Fatalf("response=%+v", response)
	}
}

func TestInteractiveModelDoneReturnsRuntimeExitCode(t *testing.T) {
	model := NewInteractiveModel(interactiveTestInfo(), &fakeTaskRunner{}, nil, nil, false)
	updated, _ := model.Update(taskResultMsg{result: TaskResult{ExitCode: 5, Detail: "已安全取消"}})
	model = updated.(InteractiveModel)
	if model.ExitCode() != 5 || !strings.Contains(model.View().Content, "已安全取消") {
		t.Fatalf("exit=%d view=%s", model.ExitCode(), model.View().Content)
	}
	updated, command := model.Update(tea.KeyPressMsg(tea.Key{Text: "q", Code: 'q'}))
	model = updated.(InteractiveModel)
	if command == nil || !model.runner.(*fakeTaskRunner).closed {
		t.Fatalf("quit=%v runner closed=%t", command, model.runner.(*fakeTaskRunner).closed)
	}
}

func interactiveTestInfo() brand.Info {
	return brand.Info{Version: "test", WorkDir: "D:/项目/梦蝶", Model: "openai-compatible:test-model", Security: "受控本地执行 · 交互审批"}
}

type fakeTaskRunner struct {
	task      string
	execution *fakeTaskExecution
	err       error
	closed    bool
}

func (runner *fakeTaskRunner) PrepareTask(task string) (TaskExecution, error) {
	runner.task = task
	if runner.execution == nil && runner.err == nil {
		runner.execution = &fakeTaskExecution{sessionID: "ses-fake"}
	}
	return runner.execution, runner.err
}

func (runner *fakeTaskRunner) Close() { runner.closed = true }

type fakeTaskExecution struct {
	sessionID string
	cancelled bool
}

func (execution *fakeTaskExecution) SessionID() string { return execution.sessionID }
func (*fakeTaskExecution) Run() TaskResult             { return TaskResult{} }
func (execution *fakeTaskExecution) Cancel()           { execution.cancelled = true }
