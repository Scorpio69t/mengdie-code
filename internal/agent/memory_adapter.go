// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

// Package agent 适配层把 memory.Retriever（spec §6.1 的三级召回实现）桥接到
// agent.MemoryRetriever（spec §6.2 的 Agent 集成接口）。该文件是生产路径
// 的默认接线：application 层把 *memory.Retririever 套上 RetrieverAdapter
// 传给 agent.Options.MemoryRetriever，Agent.Run 在第一个 turn 自动调一次
// Recall 拿到 Tier 1 catalogue 候选注入到 system 消息尾部。
package agent

import (
	"context"
	"fmt"

	"github.com/Scorpio69t/mengdie-code/internal/memory"
)

// RetrieverAdapter satisfies the agent.MemoryRetriever interface by
// translating MemoryScope into memory.Scope and dispatching the recall
// through *memory.Retriever. The adapter pins tier=1 (catalogue) because
// the first-turn catalogue section is a Tier 1 surface per spec §6.1 /
// §6.2: it is the "常驻能力说明 / 任务级主题目录" projection, not the
// FTS5 atomic surface. Tier 3 is reserved for the `memory_recall` tool
// (Task 8) so the agent can opt in to FTS5-driven lookup on demand.
//
// The cap is the lower of (topK, firstTurnCatalogueTopK) so a future
// caller that overrides topK cannot accidentally flood the catalogue
// section beyond the spec cap. Both bounds are also pinned inside
// memory.Retriever itself (tier1MaxLimit / tier3MaxTopK).
type RetrieverAdapter struct {
	retriever *memory.Retriever
}

// NewRetrieverAdapter wraps a *memory.Retriever so it satisfies
// MemoryRetriever. Nil retriever returns an adapter that always returns
// an empty hit list without error so callers can opt out of injection
// without nil-checking.
func NewRetrieverAdapter(retriever *memory.Retriever) *RetrieverAdapter {
	return &RetrieverAdapter{retriever: retriever}
}

// Recall implements MemoryRetriever. It maps the agent.MemoryScope onto
// memory.Scope and asks the underlying Retriever for the Tier 1
// catalogue. The query argument is intentionally ignored: the catalogue
// surface is query-agnostic and exists to advertise what the project
// already knows. FTS5-driven Tier 3 recall is exposed via the
// `memory_recall` tool (Task 8) so the Agent can opt in to keyword
// recall without forcing every first turn to fire FTS5.
//
// topK is clamped to firstTurnCatalogueTopK so a caller cannot widen the
// catalogue beyond the spec §6.1 cap of 20; memory.Retriever would clamp
// further at tier1MaxLimit anyway, but the explicit clamp keeps the
// rendered markdown within the 20-row bullet list the catalogue format
// advertises in its header.
func (adapter *RetrieverAdapter) Recall(ctx context.Context, _ string, topK int, scope MemoryScope) ([]MemoryHit, error) {
	if adapter == nil || adapter.retriever == nil {
		return nil, nil
	}
	memScope := memory.Scope{Kind: scope.Kind, Value: scope.Value}
	if err := memScope.Valid(); err != nil {
		return nil, fmt.Errorf("memory adapter: %w", err)
	}
	limit := topK
	if limit <= 0 || limit > firstTurnCatalogueTopK {
		limit = firstTurnCatalogueTopK
	}
	entries, err := adapter.retriever.Tier1Catalogue(ctx, memScope, limit)
	if err != nil {
		return nil, err
	}
	hits := make([]MemoryHit, 0, len(entries))
	for _, entry := range entries {
		hits = append(hits, MemoryHit{
			Memory: memory.Memory{
				ID:            entry.ID,
				Claim:         entry.Claim,
				EvidenceScore: entry.EvidenceScore,
				Authority:     entry.Authority,
			},
			Score: entry.EvidenceScore,
		})
	}
	return hits, nil
}
