// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package extractor

import (
	"context"
	"strings"

	"github.com/Scorpio69t/mengdie-code/internal/memory"
	"golang.org/x/text/unicode/norm"
)

// Hybrid 把两个 Extractor 串起来：先跑确定性 Rules，再用 LLM 候选补齐
// Rule 没覆盖的部分。LLM 是可选的（传 nil 时直接退化成纯 Rules，这是
// v0.1 LLM-disabled 模式的入口）。
//
// 去重以"Rule 先赢"为唯一原则：LLM 给出的 claim 只要在规范化后命中
// Rule 已经吐出的任何一条，就丢弃。Authority 也因此自然保留为 Rule
// 给出的那条（一般是 AuthorityRepository 或 AuthorityVerified），
// 不会因为 LLM 提议覆盖而降到 AuthorityInferred。
//
// Errors from either half are deliberately swallowed: Hybrid never blocks
// the Run. The Extractor contract is "produce as many as you can"; an
// upstream failure (missing EventReader, provider timeout) must not
// surface here. app.Runtime is the only path that touches the store.
type Hybrid struct {
	rules Extractor
	llm   Extractor // 可为 nil
}

// NewHybrid 把 rules / llm 串成一个 Extractor。两个参数都是 Extractor
// 接口（不是 *Rules / *LLM 具体类型），方便测试塞入 fake，也方便未来
// 任意 Extractor 实现接入。llm 传 nil 表示关闭 LLM。
func NewHybrid(rules, llm Extractor) *Hybrid {
	return &Hybrid{rules: rules, llm: llm}
}

// Extract 先跑 rules，再（可选地）跑 llm，按 normalizeClaim 后的 claim
// 字符串去重。返回值是全新 backing array；调用方可自由修改，不会影响
// 任何缓存。
func (h *Hybrid) Extract(ctx context.Context, sessionID string) ([]memory.Memory, error) {
	if h == nil {
		return nil, nil
	}
	rulesOut, _ := h.rules.Extract(ctx, sessionID)
	if h.llm == nil {
		return rulesOut, nil
	}
	llmOut, _ := h.llm.Extract(ctx, sessionID)
	seen := make(map[string]struct{}, len(rulesOut))
	for _, m := range rulesOut {
		seen[normalizeClaim(m.Claim)] = struct{}{}
	}
	out := append([]memory.Memory(nil), rulesOut...)
	for _, m := range llmOut {
		if _, dup := seen[normalizeClaim(m.Claim)]; dup {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

// normalizeClaim 把 claim 折成可比较的稳定形式：小写 + 去首尾空白 +
// NFC→NFD 往返，把组合字符（比如带变音符的拉丁字母）展开后再合成。
// 这一步足够让"edit_file" 和 "  EDIT_FILE " 和 "éXAMPLE" 等变体
// 在 seen map 里命中同一条 key，避免 Rule / LLM 给出的同义 claim
// 被误判成不同条目。
func normalizeClaim(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return norm.NFD.String(norm.NFC.String(s))
}
