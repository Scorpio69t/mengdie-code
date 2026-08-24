// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

// Package agent 把 memory/extractor.Extractor（spec §4 的 Rules / LLM /
// Hybrid 实现）桥接到 agent.MemoryExtractor（spec §7 的 Agent 集成接口）。
// 该文件是生产路径的默认接线：application 层把
// *memory/extractor.Hybrid（或 Rules / LLM）套上 ExtractorAdapter，传给
// agent.Options.MemoryExtractor，Agent.Run 在 Run 收尾时统一调用一次
// Extract 拿到候选，再走 Options.MemoryStore.ProposeMemory 落地。
//
// 之所以走 agent.MemoryExtractor 中转而不是直接暴露 *memory/extractor.Extractor
// 是为了：1）避免 internal/agent 反向依赖 internal/memory/extractor 的具体
// 实现（extractor.Extractor 和 agent.MemoryExtractor 签名等价，但定义在两
// 个包里，便于后续替换或裁剪）；2）让测试可以塞入 stubExtractor 这样的简
// 化实现，不必为每个测试都准备一个 *memory.Store + EventReader 的复合 fixture。
package agent

import (
	"context"

	"github.com/Scorpio69t/mengdie-code/internal/memory"
)

// ExtractorAdapter wires a memoryExtractor implementation (typically
// *memory/extractor.Hybrid / Rules / LLM) onto the agent.MemoryExtractor
// surface so Agent.Run can call it without importing the extractor
// package directly. The adapter is intentionally stateless and safe for
// concurrent use; the underlying Extractor is shared across Agent
// instances per the spec §7 wiring guidance.
type ExtractorAdapter struct {
	ext memoryExtractor
}

// NewExtractorAdapter returns an ExtractorAdapter bound to the supplied
// memoryExtractor. Passing nil returns an adapter whose Extract always
// returns (nil, nil) so callers can wire a default adapter without
// nil-checking at the call site — the applyMemoryExtraction guard in
// runtime.go already short-circuits on a nil extractor, but a wrapped
// nil here keeps the construction path safe in case the adapter is
// passed to Options directly without the Options-level guard.
func NewExtractorAdapter(ext memoryExtractor) *ExtractorAdapter {
	return &ExtractorAdapter{ext: ext}
}

// Extract forwards the call to the underlying memoryExtractor. The
// returned candidates carry the extractor-stamped Authority; Scope /
// Source defaults are re-applied by applyMemoryExtraction at the call
// site so the store's idempotency / conflict layer sees a uniform shape
// regardless of which implementation produced the rows.
func (a *ExtractorAdapter) Extract(ctx context.Context, sessionID string) ([]memory.Memory, error) {
	if a == nil || a.ext == nil {
		return nil, nil
	}
	return a.ext.Extract(ctx, sessionID)
}

// memoryExtractor is the internal surface the agent package needs from
// the memory/extractor package. Defining it here — instead of importing
// extractor.Extractor into the agent package — sidesteps a future import
// cycle if extractor.Extractor ever needs to reference agent types. The
// signature intentionally mirrors extractor.Extractor.Extract so
// *memory/extractor.Hybrid / Rules / LLM can be passed directly without
// adapter wrappers of their own.
type memoryExtractor interface {
	Extract(ctx context.Context, sessionID string) ([]memory.Memory, error)
}
