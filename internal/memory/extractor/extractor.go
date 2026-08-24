// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

// Package extractor 把已完成的 Agent Run 转成候选 memory 行。app.Runtime
// 在 run 收尾时统一注入三个实现之一：
//
//   - Rules: 扫描 session events 里的结构化模式（工具名、test/lint 命令）。
//     零 LLM 成本、完全确定性；是默认实现。
//   - LLM: 从 transcript 抽取，由 provider 选型决定，Task 5 落地。
//   - Hybrid: 先跑 Rules，再用 LLM 候选补齐 Rule 没覆盖的部分（Task 5）。
//
// 实现 MUST NOT 写入 store —— app.Runtime 是唯一的写入路径，统一经过
// spec §4.1 的 Authority↔SourceType 守门与 §4.2 的 idempotency /
// 冲突状态机。
package extractor

import (
	"context"

	"github.com/Scorpio69t/mengdie-code/internal/memory"
)

// Extractor returns candidate memory rows derived from a completed run.
// Implementations MUST NOT write to the store; app.Runtime is the single
// write path so trust gates (Authority 守门 + Conflict 状态机) are
// applied uniformly across Rules / LLM / Hybrid.
type Extractor interface {
	// Extract reads the session identified by sessionID and returns the
	// candidate memory rows the implementation produced. The slice's
	// Authority field is already set; Scope / Source / Kind are
	// re-applied by app.Runtime before Save so the store's idempotency
	// layer sees a uniform shape.
	Extract(ctx context.Context, sessionID string) ([]memory.Memory, error)
}
