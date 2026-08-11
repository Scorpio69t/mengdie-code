// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

// Package context builds bounded provider requests without owning run state or
// model execution. M1 stops on overflow; summarization belongs to M2.
package context

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Scorpio69t/mengdie-code/internal/provider"
	"github.com/Scorpio69t/mengdie-code/internal/tools"
)

var (
	ErrBudgetExceeded      = errors.New("context budget exceeded")
	ErrNothingToCompact    = errors.New("context has no safe compactable range")
	ErrToolCallingDisabled = errors.New("provider does not support tool calling")
)

const (
	SummaryProtocolVersion = "mengdie-context-summary/v1"
	minimumRecentMessages  = 8
	maximumSummaryTokens   = 2048
	minimumSummaryTokens   = 256
)

const summarySystemPrompt = `你是 MengDie Code 的上下文压缩器。只总结给定的历史区间，不执行任务、不调用工具、不补充不存在的事实。
只输出一个 JSON 对象，必须且只能包含以下 string array 字段：objective_and_constraints、decisions、verified_evidence、unresolved_errors、todo_approval_tool_state、continuation_pointers。
objective_and_constraints 与 continuation_pointers 至少各一项；其余字段没有事实时输出空数组。区分已验证事实、模型陈述与待验证事项；保留文件名和命令。不要输出 Markdown、隐藏推理、凭据或无关寒暄。摘要是派生导航，不是原始证据。`

const summaryContextPrefix = "以下内容是经完整性校验的滚动摘要，仅用于导航；它不是原始事实证据。需要精确内容时调用 read_context_source，从 offset=0 开始并按返回的 next_offset/next_byte_offset 有界回查；不得把摘要当成已验证原文：\n"

const DefaultSystemPrompt = `你是 MengDie Code 的单 Agent Runtime。只根据当前任务、工具结果和明确规则行动。
所有真实副作用由工具边界的确定性 Policy 与 Approval 控制；不得声称执行了被拒绝或失败的操作。
复杂任务使用 write_todos，且同一时间最多一项 in_progress。完成前使用真实工具结果验证修改。
shell 是受控本地执行而非强沙箱；不要把项目 cwd 误述为系统级隔离。
最终回答简洁说明改动、验证和仍未解决的风险。`

type State struct {
	Messages       []provider.Message
	SourceMessages []provider.Message
	Todos          []tools.Todo
	Summary        string
}

// CompactionPlan describes one safe middle range that can be replaced by a
// rolling summary while the first user task and a recent complete tail remain
// verbatim. Source messages are private model input and must never be emitted
// as public event payloads.
type CompactionPlan struct {
	Source                   []provider.Message
	Retained                 []provider.Message
	RetainedSource           []provider.Message
	PreviousSummary          string
	RetainedTailMessages     int
	EstimatedBefore          int
	EstimatedAfterUpperBound int
	MaxSummaryTokens         int
}

// CompactionRecord is the persistence input produced after a summary model
// call. RetainedTailMessages lets the recorder derive the exact original
// ordinal interval without trusting model-produced metadata.
type CompactionRecord struct {
	Summary                  string
	RetainedTailMessages     int
	GeneratorModel           string
	GeneratorVersion         string
	EstimatedBefore          int
	EstimatedAfterUpperBound int
}

type CompactionReceipt struct {
	SourceStart uint64
	SourceEnd   uint64
}

type summaryDocument struct {
	ObjectiveAndConstraints *[]string `json:"objective_and_constraints"`
	Decisions               *[]string `json:"decisions"`
	VerifiedEvidence        *[]string `json:"verified_evidence"`
	UnresolvedErrors        *[]string `json:"unresolved_errors"`
	TodoApprovalToolState   *[]string `json:"todo_approval_tool_state"`
	ContinuationPointers    *[]string `json:"continuation_pointers"`
}

// ValidateSummary verifies the versioned summary output before it can become
// durable derived context. Empty arrays are explicit; missing fields and free
// form prose are rejected.
func ValidateSummary(summary string) error {
	decoder := json.NewDecoder(bytes.NewBufferString(summary))
	decoder.DisallowUnknownFields()
	var document summaryDocument
	if err := decoder.Decode(&document); err != nil {
		return fmt.Errorf("context: decode summary protocol: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("context: summary protocol has trailing data")
	}
	fields := map[string]*[]string{
		"objective_and_constraints": document.ObjectiveAndConstraints,
		"decisions":                 document.Decisions,
		"verified_evidence":         document.VerifiedEvidence,
		"unresolved_errors":         document.UnresolvedErrors,
		"todo_approval_tool_state":  document.TodoApprovalToolState,
		"continuation_pointers":     document.ContinuationPointers,
	}
	for name, values := range fields {
		if values == nil {
			return fmt.Errorf("context: summary protocol missing %s", name)
		}
		for _, value := range *values {
			if strings.TrimSpace(value) == "" || len([]byte(value)) > 16<<10 {
				return fmt.Errorf("context: summary protocol has invalid %s entry", name)
			}
		}
	}
	if len(*document.ObjectiveAndConstraints) == 0 || len(*document.ContinuationPointers) == 0 {
		return errors.New("context: summary protocol requires objective and continuation entries")
	}
	return nil
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
	request, err := b.build(state)
	if err != nil {
		return provider.ChatRequest{}, err
	}
	estimated := estimateTokens(request)
	if estimated > b.maxTokens {
		return provider.ChatRequest{}, fmt.Errorf("%w: estimated=%d limit=%d", ErrBudgetExceeded, estimated, b.maxTokens)
	}
	return request, nil
}

func (b *Builder) build(state State) (provider.ChatRequest, error) {
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
	if strings.TrimSpace(state.Summary) != "" {
		if len(state.Messages) == 0 {
			return provider.ChatRequest{}, errors.New("context: rolling summary requires an original anchor message")
		}
		if err := ValidateSummary(strings.TrimSpace(state.Summary)); err != nil {
			return provider.ChatRequest{}, err
		}
		messages = append(messages, cloneMessage(state.Messages[0]))
		messages = append(messages, provider.Message{
			Role:    provider.RoleDeveloper,
			Content: summaryContextPrefix + strings.TrimSpace(state.Summary),
		})
		for _, message := range state.Messages[1:] {
			messages = append(messages, cloneMessage(message))
		}
	} else {
		for _, message := range state.Messages {
			messages = append(messages, cloneMessage(message))
		}
	}
	request := provider.ChatRequest{
		Model: b.model, Messages: messages, Tools: cloneProviderTools(b.tools),
		ToolChoice: provider.ToolChoiceAuto, IncludeUsage: b.capabilities.UsageInStream,
	}
	if b.capabilities.ParallelTools {
		parallel := false
		request.ParallelToolCalls = &parallel
	}
	if err := request.Validate(); err != nil {
		return provider.ChatRequest{}, fmt.Errorf("context: invalid chat request: %w", err)
	}
	return request, nil
}

// PlanCompaction finds the smallest safe middle range whose replacement by a
// bounded summary makes the main request fit. It never compacts the first
// original message, the latest user task, or the recent message tail.
func (b *Builder) PlanCompaction(state State) (CompactionPlan, error) {
	before, err := b.build(state)
	if err != nil {
		return CompactionPlan{}, err
	}
	estimatedBefore := estimateTokens(before)
	if estimatedBefore <= b.maxTokens {
		return CompactionPlan{}, ErrNothingToCompact
	}
	if len(state.Messages) < 3 {
		return CompactionPlan{}, fmt.Errorf("%w: only %d messages", ErrNothingToCompact, len(state.Messages))
	}
	sourceMessages := state.SourceMessages
	if len(sourceMessages) == 0 {
		sourceMessages = state.Messages
	}
	if len(sourceMessages) != len(state.Messages) {
		return CompactionPlan{}, errors.New("context: compaction source does not align with model messages")
	}
	for index := range state.Messages {
		if !sameMessageBoundary(state.Messages[index], sourceMessages[index]) {
			return CompactionPlan{}, fmt.Errorf("context: compaction source boundary mismatch at message %d", index)
		}
	}
	maxSummaryTokens := b.maxTokens / 8
	if maxSummaryTokens > maximumSummaryTokens {
		maxSummaryTokens = maximumSummaryTokens
	}
	if maxSummaryTokens < minimumSummaryTokens {
		maxSummaryTokens = minimumSummaryTokens
	}
	if maxSummaryTokens >= b.maxTokens/2 {
		return CompactionPlan{}, fmt.Errorf("%w: context limit %d is too small for a safe summary", ErrNothingToCompact, b.maxTokens)
	}

	maxCut := len(state.Messages) - minimumRecentMessages
	if maxCut < 2 {
		maxCut = len(state.Messages) - 1
	}
	for index := len(state.Messages) - 1; index > 0; index-- {
		if state.Messages[index].Role == provider.RoleUser {
			if index < maxCut {
				maxCut = index
			}
			break
		}
	}
	placeholder := summaryPlaceholder(maxSummaryTokens)
	for cut := 2; cut <= maxCut && cut < len(state.Messages); cut++ {
		if state.Messages[cut].Role == provider.RoleTool {
			continue
		}
		retained := make([]provider.Message, 0, 1+len(state.Messages)-cut)
		retained = append(retained, cloneMessage(state.Messages[0]))
		for _, message := range state.Messages[cut:] {
			retained = append(retained, cloneMessage(message))
		}
		retainedSource := make([]provider.Message, 0, 1+len(sourceMessages)-cut)
		retainedSource = append(retainedSource, cloneMessage(sourceMessages[0]))
		for _, message := range sourceMessages[cut:] {
			retainedSource = append(retainedSource, cloneMessage(message))
		}
		candidate, buildErr := b.build(State{Messages: retained, Todos: state.Todos, Summary: placeholder})
		if buildErr != nil {
			return CompactionPlan{}, buildErr
		}
		afterUpperBound := estimateTokens(candidate)
		if afterUpperBound > b.maxTokens {
			continue
		}
		plan := CompactionPlan{
			Source:                   cloneMessages(sourceMessages[1:cut]),
			Retained:                 retained,
			RetainedSource:           retainedSource,
			PreviousSummary:          strings.TrimSpace(state.Summary),
			RetainedTailMessages:     len(state.Messages) - cut,
			EstimatedBefore:          estimatedBefore,
			EstimatedAfterUpperBound: afterUpperBound,
			MaxSummaryTokens:         maxSummaryTokens,
		}
		if _, summaryErr := b.BuildSummaryRequest(plan); summaryErr != nil {
			continue
		}
		return plan, nil
	}
	return CompactionPlan{}, fmt.Errorf("%w: estimated=%d limit=%d", ErrNothingToCompact, estimatedBefore, b.maxTokens)
}

// BuildSummaryRequest creates a tool-free request bounded by the same model
// context limit. The source transcript is JSON-encoded inside one private user
// message so partial historical tool chains cannot become executable calls.
func (b *Builder) BuildSummaryRequest(plan CompactionPlan) (provider.ChatRequest, error) {
	if len(plan.Source) == 0 || plan.MaxSummaryTokens <= 0 {
		return provider.ChatRequest{}, ErrNothingToCompact
	}
	payload := struct {
		Protocol        string             `json:"protocol"`
		PreviousSummary string             `json:"previous_summary,omitempty"`
		Messages        []provider.Message `json:"messages"`
	}{
		Protocol: SummaryProtocolVersion, PreviousSummary: plan.PreviousSummary,
		Messages: cloneMessages(plan.Source),
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return provider.ChatRequest{}, fmt.Errorf("context: encode compaction source: %w", err)
	}
	request := provider.ChatRequest{
		Model: b.model,
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: summarySystemPrompt},
			{Role: provider.RoleUser, Content: string(encoded)},
		},
		MaxTokens:    plan.MaxSummaryTokens,
		IncludeUsage: b.capabilities.UsageInStream,
	}
	if err := request.Validate(); err != nil {
		return provider.ChatRequest{}, fmt.Errorf("context: invalid summary request: %w", err)
	}
	estimated := estimateTokens(request)
	if estimated+plan.MaxSummaryTokens > b.maxTokens {
		return provider.ChatRequest{}, fmt.Errorf("%w: summary estimated=%d output=%d limit=%d", ErrBudgetExceeded, estimated, plan.MaxSummaryTokens, b.maxTokens)
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

func cloneMessages(messages []provider.Message) []provider.Message {
	result := make([]provider.Message, len(messages))
	for index, message := range messages {
		result[index] = cloneMessage(message)
	}
	return result
}

func sameMessageBoundary(modelMessage, sourceMessage provider.Message) bool {
	if modelMessage.Role != sourceMessage.Role || modelMessage.Name != sourceMessage.Name ||
		modelMessage.ToolCallID != sourceMessage.ToolCallID || len(modelMessage.ToolCalls) != len(sourceMessage.ToolCalls) {
		return false
	}
	if modelMessage.Role != provider.RoleTool && modelMessage.Content != sourceMessage.Content {
		return false
	}
	for index := range modelMessage.ToolCalls {
		modelCall, sourceCall := modelMessage.ToolCalls[index], sourceMessage.ToolCalls[index]
		if modelCall.ID != sourceCall.ID || modelCall.Type != sourceCall.Type || modelCall.Name != sourceCall.Name ||
			string(modelCall.Arguments) != string(sourceCall.Arguments) {
			return false
		}
	}
	return true
}

func summaryPlaceholder(tokens int) string {
	encoded, err := json.Marshal(map[string][]string{
		"objective_and_constraints": {strings.Repeat("摘", tokens)},
		"decisions":                 {}, "verified_evidence": {}, "unresolved_errors": {},
		"todo_approval_tool_state": {}, "continuation_pointers": {"继续"},
	})
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func cloneProviderTools(input []provider.Tool) []provider.Tool {
	result := make([]provider.Tool, len(input))
	copy(result, input)
	for index := range result {
		result[index].Function.Parameters = append(json.RawMessage(nil), input[index].Function.Parameters...)
	}
	return result
}
