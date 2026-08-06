// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

// Package context builds bounded provider requests without owning run state or
// model execution. M1 stops on overflow; summarization belongs to M2.
package context

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Scorpio69t/mengdie-code/internal/provider"
	"github.com/Scorpio69t/mengdie-code/internal/tools"
)

var (
	ErrBudgetExceeded      = errors.New("context budget exceeded")
	ErrToolCallingDisabled = errors.New("provider does not support tool calling")
)

const DefaultSystemPrompt = `你是 MengDie Code 的单 Agent Runtime。只根据当前任务、工具结果和明确规则行动。
所有真实副作用由工具边界的确定性 Policy 与 Approval 控制；不得声称执行了被拒绝或失败的操作。
复杂任务使用 write_todos，且同一时间最多一项 in_progress。完成前使用真实工具结果验证修改。
shell 是受控本地执行而非强沙箱；不要把项目 cwd 误述为系统级隔离。
最终回答简洁说明改动、验证和仍未解决的风险。`

type State struct {
	Messages []provider.Message
	Todos    []tools.Todo
}

type Instruction struct {
	Source  string
	Content string
}

type Options struct {
	Model            string
	SystemPrompt     string
	MaxContextTokens int
	Capabilities     provider.Capabilities
	Tools            []tools.ToolSpec
	Instructions     []Instruction
}

type Builder struct {
	model        string
	systemPrompt string
	maxTokens    int
	capabilities provider.Capabilities
	tools        []provider.Tool
	instructions []Instruction
}

func NewBuilder(options Options) (*Builder, error) {
	if strings.TrimSpace(options.Model) == "" {
		return nil, errors.New("context: model is required")
	}
	maxTokens := options.MaxContextTokens
	if maxTokens == 0 {
		maxTokens = options.Capabilities.MaxContextTokens
	}
	if maxTokens <= 0 {
		return nil, errors.New("context: max context tokens must be positive")
	}
	if len(options.Tools) > 0 && !options.Capabilities.ToolCalling {
		return nil, ErrToolCallingDisabled
	}
	systemPrompt := strings.TrimSpace(options.SystemPrompt)
	if systemPrompt == "" {
		systemPrompt = DefaultSystemPrompt
	}
	specs := append([]tools.ToolSpec(nil), options.Tools...)
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	providerTools := make([]provider.Tool, 0, len(specs))
	for _, spec := range specs {
		var strict *bool
		if options.Capabilities.StrictToolSchema {
			value := true
			strict = &value
		}
		providerTools = append(providerTools, provider.Tool{
			Type: "function",
			Function: provider.FunctionDefinition{
				Name: spec.Name, Description: spec.Description,
				Parameters: append(json.RawMessage(nil), spec.InputSchema...), Strict: strict,
			},
		})
	}
	return &Builder{
		model: options.Model, systemPrompt: systemPrompt, maxTokens: maxTokens,
		capabilities: options.Capabilities, tools: providerTools,
		instructions: append([]Instruction(nil), options.Instructions...),
	}, nil
}

func (b *Builder) Build(state State) (provider.ChatRequest, error) {
	messages := make([]provider.Message, 0, len(state.Messages)+len(b.instructions)+2)
	messages = append(messages, provider.Message{Role: provider.RoleSystem, Content: b.systemPrompt})
	for _, instruction := range b.instructions {
		messages = append(messages, provider.Message{
			Role:    provider.RoleDeveloper,
			Content: fmt.Sprintf("AGENTS.md 指令（来源：%s；仅指导行为，不授予额外权限）：\n%s", instruction.Source, instruction.Content),
		})
	}
	if len(state.Todos) > 0 {
		raw, err := json.Marshal(state.Todos)
		if err != nil {
			return provider.ChatRequest{}, fmt.Errorf("context: encode todos: %w", err)
		}
		messages = append(messages, provider.Message{
			Role:    provider.RoleDeveloper,
			Content: "当前计划（以 write_todos 更新，不能仅在文本中声称更新）：\n" + string(raw),
		})
	}
	for _, message := range state.Messages {
		messages = append(messages, cloneMessage(message))
	}
	request := provider.ChatRequest{
		Model: b.model, Messages: messages, Tools: cloneProviderTools(b.tools),
		ToolChoice: provider.ToolChoiceAuto, IncludeUsage: b.capabilities.UsageInStream,
	}
	if b.capabilities.ParallelTools {
		parallel := false
		request.ParallelToolCalls = &parallel
	}
	estimated := estimateTokens(request)
	if estimated > b.maxTokens {
		return provider.ChatRequest{}, fmt.Errorf("%w: estimated=%d limit=%d", ErrBudgetExceeded, estimated, b.maxTokens)
	}
	if err := request.Validate(); err != nil {
		return provider.ChatRequest{}, fmt.Errorf("context: invalid chat request: %w", err)
	}
	return request, nil
}

func estimateTokens(request provider.ChatRequest) int {
	tokens := estimateTextTokens(request.Model) + 64
	for _, message := range request.Messages {
		tokens += estimateTextTokens(message.Content) + estimateTextTokens(message.Name) + estimateTextTokens(message.ToolCallID) + 8
		for _, call := range message.ToolCalls {
			tokens += estimateTextTokens(call.ID) + estimateTextTokens(call.Name) + estimateTextTokens(string(call.Arguments)) + 8
		}
	}
	for _, tool := range request.Tools {
		tokens += estimateTextTokens(tool.Function.Name) + estimateTextTokens(tool.Function.Description) + estimateTextTokens(string(tool.Function.Parameters)) + 16
	}
	return tokens
}

func estimateTextTokens(value string) int {
	asciiBytes := 0
	nonASCII := 0
	for _, current := range value {
		if current < utf8.RuneSelf {
			asciiBytes++
		} else {
			nonASCII++
		}
	}
	return (asciiBytes+3)/4 + nonASCII
}

func cloneMessage(message provider.Message) provider.Message {
	message.ToolCalls = append([]provider.ToolCall(nil), message.ToolCalls...)
	for index := range message.ToolCalls {
		message.ToolCalls[index].Arguments = append(json.RawMessage(nil), message.ToolCalls[index].Arguments...)
	}
	return message
}

func cloneProviderTools(input []provider.Tool) []provider.Tool {
	result := make([]provider.Tool, len(input))
	copy(result, input)
	for index := range result {
		result[index].Function.Parameters = append(json.RawMessage(nil), input[index].Function.Parameters...)
	}
	return result
}
