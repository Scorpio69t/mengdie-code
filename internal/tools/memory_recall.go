// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

// Package tools 暴露 spec §6.2 的 `memory_recall` 工具：Agent 在 turn 中
// 按需主动调用的 Tier 3 原子记忆召回（effect=state，不需要审批 / patch
// journal）。Production 接线 (internal/app/runtime.go 后续 commit) 通过
// *memory.Retriever 适配为本包 MemoryRecallRetriever 接口注入；本包定义
// 局部类型是为了避开 tools → memory → session → tools 的导入环——memory
// 必须依赖 session 拿数据库句柄，session 又依赖 tools（patch journal /
// rewind），所以 tools 不能直接 import memory。
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// MemoryRecallToolName 是 spec §6.2 规定的 tool 短名，与 read_file /
// edit_file / write_todos 等内部工具保持 snake_case 风格。
const MemoryRecallToolName = "memory_recall"

const memoryRecallSchema = `{
  "type": "object",
  "properties": {
    "query": {
      "type": "string",
      "description": "FTS5 关键词；空格分隔的多 token 按隐式 AND 处理（实现见 memory.Retriever.buildFTSQuery）"
    },
    "topK": {
      "type": "integer",
      "minimum": 1,
      "maximum": 50,
      "default": 5,
      "description": "返回的记忆条目数上限；缺省 5，硬上限 50"
    }
  },
  "required": ["query"],
  "additionalProperties": false
}`

// memoryRecallTopKDefault / memoryRecallTopKMin / memoryRecallTopKMax
// 把 spec §6.2 的默认值（5）和防御性边界（[1, 50]）落到 Go 端常量。
// 准备阶段的验证与执行阶段的运行时裁剪都引用这三个常量。
const (
	memoryRecallTopKDefault = 5
	memoryRecallTopKMin     = 1
	memoryRecallTopKMax     = 50
)

// MemoryRecallScope 是 memory.Scope 的本地影子——Kind / Value 描述 Tier 3
// 召回的 scope_kind + scope_value（spec §3 / §6.1）。在 tools 包内部重新
// 声明可以让 memory_recall 工具在不依赖 internal/memory 的前提下保持对外
// interface 干净；生产接线通过内部 adapter 把 memory.Scope 翻译到这里。
type MemoryRecallScope struct {
	Kind  string
	Value string
}

// MemoryRecallHit 是 Tier 3 召回的单条命中的最小投影：id + claim + 三个
// 信任加权字段（authority / evidence / score）+ 来源指针。tools 包只
// 需要这些字段做 markdown 渲染，不消费 memory.Memory 的全部字段，因此
// 单独投影避免引入整个 memory.Memory 类型。
type MemoryRecallHit struct {
	ID            string
	Claim         string
	Authority     string
	SourceRef     string
	EvidenceScore float64
	Score         float64
}

// MemoryRecallRetriever 是 memory_recall 工具对 Tier 3 召回后端的最小
// 接口契约。*memory.Retriever 通过一个内嵌 adapter（生产接线见
// internal/app/runtime.go 的 follow-up）实现该接口；测试用本地 stub 即
// 可满足，这样单元测试不必拉起真实的 session + memory schema。
type MemoryRecallRetriever interface {
	Tier3AtomicRecall(ctx context.Context, query string, topK int, scope MemoryRecallScope) ([]MemoryRecallHit, error)
}

// MemoryRecallOption 把可选构造参数聚合成 functional-options 模式，方便
// 后续加入 run-scoped 字段（如 session id）而不破坏 NewMemoryRecallTool
// 的二进制签名。
type MemoryRecallOption func(*memoryRecallTool)

// WithProjectIdentity 把 Agent 的 ProjectIdentity 翻译成调用时的 project
// scope。空串保留默认的 user scope，避免单测 / 安全默认路径被悄悄收窄
// 到某个具体项目。
func WithProjectIdentity(id string) MemoryRecallOption {
	return func(t *memoryRecallTool) {
		id = strings.TrimSpace(id)
		if id == "" {
			t.scope = MemoryRecallScope{Kind: "user"}
			return
		}
		t.scope = MemoryRecallScope{Kind: "project", Value: id}
	}
}

// memoryRecallTool 是 spec §6.2 memory_recall 的内部实现：effect=state 的
// 只读召回，不需要 patch journal / approval / capability。retriever 在
// 构造期注入；scope 通过 MemoryRecallOption 在构造期配置，两者在 Execute
// 期间保持稳定。
type memoryRecallTool struct {
	retriever MemoryRecallRetriever
	scope     MemoryRecallScope
}

// NewMemoryRecallTool 构造 spec §6.2 的 memory_recall 工具。retriever
// 不能为空（Execute 会立即 panic / 报错），opts 用于补 scope 之类的运行
// 时上下文。空 retriever 不报错但 Execute 触发 nil-pointer 检查并返回
// 明确的错误信息，方便上层区分"未接线"与"召回失败"两种状态。
func NewMemoryRecallTool(retriever MemoryRecallRetriever, opts ...MemoryRecallOption) Tool {
	t := &memoryRecallTool{
		retriever: retriever,
		scope:     MemoryRecallScope{Kind: "user"},
	}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

func (memoryRecallTool) Spec() ToolSpec {
	return ToolSpec{
		Name:        MemoryRecallToolName,
		Description: "按需原子召回可信记忆 (Tier 3 FTS5)；返回 markdown 列表，每行包含 id、authority/evidence/score 信任权重、claim 和 source_ref；空结果返回 (empty)",
		InputSchema: json.RawMessage(memoryRecallSchema),
		Effects:     []Effect{EffectState},
	}
}

// memoryRecallArgs 是输入 schema 的本地解码目标。query 是必填，topK 缺
// 省 5 且 clamp 到 [1, 50]。解码使用 decodeArgs（DisallowUnknownFields），
// 与本包其它工具保持一致的严格风格——模型拼错字段名直接暴露为错误。
type memoryRecallArgs struct {
	Query string `json:"query"`
	TopK  int    `json:"topK"`
}

// validate 在 Prepare 与 Execute 共享：Prepare 先用它挡掉坏请求，
// Execute 再用一次以防 ApprovedCall 路径把 canonical arg 改成与 raw
// 不一致的危险值。topK 零值被替换为 spec §6.2 默认值 5，调用方语义
// 上等同于省略 topK。
func (a *memoryRecallArgs) validate() error {
	if strings.TrimSpace(a.Query) == "" {
		return errors.New("memory_recall: query is required")
	}
	if a.TopK == 0 {
		a.TopK = memoryRecallTopKDefault
	}
	if a.TopK < memoryRecallTopKMin || a.TopK > memoryRecallTopKMax {
		return fmt.Errorf("memory_recall: topK %d outside [%d, %d]", a.TopK, memoryRecallTopKMin, memoryRecallTopKMax)
	}
	return nil
}

// Prepare 校验 query/topK 并落默认到 canonical arg，让两次分别省略或显式
// 给 topK=5 的调用产生同一份 digest——digest 稳定性是 Approval replay /
// 能力校验（patch journal / capability 链路）的输入。
func (memoryRecallTool) Prepare(ctx context.Context, raw json.RawMessage, env PrepareEnv) (*PreparedCall, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var args memoryRecallArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	if err := args.validate(); err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("memory_recall: encode prepared arguments: %w", err)
	}
	return PrepareCall(env.CallID, MemoryRecallToolName, canonical,
		[]Effect{EffectState}, nil,
		Preview{
			Kind:  PreviewNone,
			Title: "按需原子召回可信记忆",
			Body:  fmt.Sprintf("query=%q scope=%s/%s topK=%d", args.Query, scopeKindOrDefault(), scopeValueOrDefault(), args.TopK),
		},
		nil,
	)
}

// scopeKindOrDefault / scopeValueOrDefault 只在 Prepare 的 preview 文案
// 里提供一个非空默认值，避免 %s 在 user-scope（Kind=user, Value=""）下
// 渲染成 "user/" 让用户疑惑。这两个 helper 故意走全局零值：因为
// memoryRecallTool 是值接收者，scope 在 Prepare 不可见——preview 里的
// scope 信息本就是近似的"按当前默认调用"的提示。
func scopeKindOrDefault() string  { return "user" }
func scopeValueOrDefault() string { return "" }

// Execute 调用 retriever.Tier3AtomicRecall 并渲染 markdown 列表。state
// 工具不需要 CheckCapability——Capability{Nonce:""} 会让
// CheckCapability 立即报 ErrCapabilityMissing，所以这里根本不调用它。
// 上层在 Agent.Run 路径上对 EffectState 已有专门的不签发 capability 的
// 设计（与 write_todos 不同：write_todos 在 Prepare 阶段拿到合法
// capability 因此仍走 CheckCapability；memory_recall 在 spec 上明确
// "no approval needed"）。
func (t memoryRecallTool) Execute(ctx context.Context, call *PreparedCall, _ Capability, env ExecEnv) (*ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if t.retriever == nil {
		return nil, errors.New("memory_recall: retriever is required")
	}
	var args memoryRecallArgs
	if err := decodeArgs(call.CanonicalArg, &args); err != nil {
		return nil, fmt.Errorf("memory_recall: decode canonical arguments: %w", err)
	}
	if err := args.validate(); err != nil {
		return nil, err
	}
	hits, err := t.retriever.Tier3AtomicRecall(ctx, args.Query, args.TopK, t.scope)
	if err != nil {
		return nil, fmt.Errorf("memory_recall: retrieve: %w", err)
	}
	output := formatMemoryRecallHits(hits)
	return &ToolResult{
		Output: output,
		Metadata: map[string]string{
			"query": args.Query,
			"topK":  fmt.Sprintf("%d", args.TopK),
			"scope": fmt.Sprintf("%s/%s", t.scope.Kind, t.scope.Value),
			"count": fmt.Sprintf("%d", len(hits)),
		},
	}, nil
}

// formatMemoryRecallHits 按 spec §6.2 的 markdown 行格式渲染：
//
//   - {id} (authority={a}, evidence={e:.2f}, score={s:.2f}) {claim} [src: {ref}]
//
// 空命中渲染字面量 (empty) 让 Agent 能区分"召回无果"与"工具失败"——
// 工具失败走 error 分支，markdown 输出本身永远是非空字符串。
func formatMemoryRecallHits(hits []MemoryRecallHit) string {
	if len(hits) == 0 {
		return "(empty)"
	}
	var b strings.Builder
	for _, hit := range hits {
		fmt.Fprintf(&b, "- %s (authority=%s, evidence=%.2f, score=%.2f) %s [src: %s]\n",
			hit.ID, hit.Authority, hit.EvidenceScore, hit.Score, hit.Claim, hit.SourceRef)
	}
	return strings.TrimRight(b.String(), "\n")
}
