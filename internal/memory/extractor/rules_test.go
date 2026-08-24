// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package extractor

import (
	"context"
	"testing"

	"github.com/Scorpio69t/mengdie-code/internal/memory"
)

// TestRuleEditFile covers the edit_file → repository trigger: at least one
// successful tool.completed with name=edit_file must produce a single
// AuthorityRepository memory claiming the project uses edit_file.
func TestRuleEditFile(t *testing.T) {
	events := []eventRow{
		{Kind: "tool.completed", Name: "edit_file", Success: true},
	}
	got := ruleEditFile(events)
	if len(got) != 1 {
		t.Fatalf("want 1 memory, got %d", len(got))
	}
	if got[0].Authority != memory.AuthorityRepository {
		t.Fatalf("want authority=%s, got %s", memory.AuthorityRepository, got[0].Authority)
	}
	if got[0].Claim != "项目使用 edit_file 修改文件" {
		t.Fatalf("want claim %q, got %q", "项目使用 edit_file 修改文件", got[0].Claim)
	}
}

// TestRuleEditFileSkipsFailed confirms a failed edit_file invocation must
// not produce the repository claim.
func TestRuleEditFileSkipsFailed(t *testing.T) {
	events := []eventRow{
		{Kind: "tool.completed", Name: "edit_file", Success: false},
	}
	if got := ruleEditFile(events); got != nil {
		t.Fatalf("expected no memory for failed edit_file, got %+v", got)
	}
}

// TestRuleWriteFile covers the write_file → repository trigger.
func TestRuleWriteFile(t *testing.T) {
	events := []eventRow{
		{Kind: "tool.completed", Name: "write_file", Success: true},
	}
	got := ruleWriteFile(events)
	if len(got) != 1 {
		t.Fatalf("want 1 memory, got %d", len(got))
	}
	if got[0].Authority != memory.AuthorityRepository {
		t.Fatalf("want authority=%s, got %s", memory.AuthorityRepository, got[0].Authority)
	}
	if got[0].Claim != "项目使用 write_file 创建或覆盖文件" {
		t.Fatalf("want claim %q, got %q", "项目使用 write_file 创建或覆盖文件", got[0].Claim)
	}
}

// TestRuleGoTest covers the shell+go-test → verified trigger. The command
// text lives in SourceRef per the brief; ruleGoTest is permissive about the
// rest of the command line.
func TestRuleGoTest(t *testing.T) {
	events := []eventRow{
		{Kind: "tool.completed", Name: "shell", Success: true, SourceRef: "go test ./..."},
	}
	got := ruleGoTest(events)
	if len(got) != 1 {
		t.Fatalf("want 1 memory, got %d", len(got))
	}
	if got[0].Authority != memory.AuthorityVerified {
		t.Fatalf("want authority=%s, got %s", memory.AuthorityVerified, got[0].Authority)
	}
	if got[0].Claim != "项目测试入口是 go test ./..." {
		t.Fatalf("want claim %q, got %q", "项目测试入口是 go test ./...", got[0].Claim)
	}
}

// TestRuleGoTestIgnoresUnrelatedCommands confirms the rule does not fire on
// shell commands that happen to share the prefix but are not test commands.
func TestRuleGoTestIgnoresUnrelatedCommands(t *testing.T) {
	events := []eventRow{
		{Kind: "tool.completed", Name: "shell", Success: true, SourceRef: "go build ./..."},
	}
	if got := ruleGoTest(events); got != nil {
		t.Fatalf("expected no memory for non-test command, got %+v", got)
	}
}

// TestRuleGoLint covers the shell+golangci-lint → verified trigger.
func TestRuleGoLint(t *testing.T) {
	events := []eventRow{
		{Kind: "tool.completed", Name: "shell", Success: true, SourceRef: "golangci-lint run"},
	}
	got := ruleGoLint(events)
	if len(got) != 1 {
		t.Fatalf("want 1 memory, got %d", len(got))
	}
	if got[0].Authority != memory.AuthorityVerified {
		t.Fatalf("want authority=%s, got %s", memory.AuthorityVerified, got[0].Authority)
	}
	if got[0].Claim != "项目使用 golangci-lint 做静态检查" {
		t.Fatalf("want claim %q, got %q", "项目使用 golangci-lint 做静态检查", got[0].Claim)
	}
}

// TestRuleRunAllSuccess covers the inferred trigger: a run.completed event
// with zero failed tool.completed events produces a single
// AuthorityInferred memory.
func TestRuleRunAllSuccess(t *testing.T) {
	events := []eventRow{
		{Kind: "tool.completed", Name: "edit_file", Success: true},
		{Kind: "tool.completed", Name: "shell", Success: true, SourceRef: "go test ./..."},
		{Kind: "run.completed"},
	}
	got := ruleRunAllSuccess(events)
	if len(got) != 1 {
		t.Fatalf("want 1 memory, got %d", len(got))
	}
	if got[0].Authority != memory.AuthorityInferred {
		t.Fatalf("want authority=%s, got %s", memory.AuthorityInferred, got[0].Authority)
	}
	if got[0].Claim != "本次 Agent Run 整体成功" {
		t.Fatalf("want claim %q, got %q", "本次 Agent Run 整体成功", got[0].Claim)
	}
}

// TestRuleRunAllSuccessRequiresRunCompleted confirms the rule stays silent
// when no run.completed event is present even if all tools succeeded.
func TestRuleRunAllSuccessRequiresRunCompleted(t *testing.T) {
	events := []eventRow{
		{Kind: "tool.completed", Name: "edit_file", Success: true},
	}
	if got := ruleRunAllSuccess(events); got != nil {
		t.Fatalf("expected no memory without run.completed, got %+v", got)
	}
}

// TestRuleRunAllSuccessRejectsFailedTool confirms a single failed
// tool.completed disqualifies the run even when run.completed is present.
func TestRuleRunAllSuccessRejectsFailedTool(t *testing.T) {
	events := []eventRow{
		{Kind: "tool.completed", Name: "edit_file", Success: true},
		{Kind: "tool.completed", Name: "shell", Success: false, SourceRef: "go test ./..."},
		{Kind: "run.completed"},
	}
	if got := ruleRunAllSuccess(events); got != nil {
		t.Fatalf("expected no memory when a tool failed, got %+v", got)
	}
}

// TestRuleProviderProtocolFailures covers the ≥2 run.failed with
// category=provider_protocol → inferred trigger. The category lives in
// SourceRef per the brief's placeholder shape.
func TestRuleProviderProtocolFailures(t *testing.T) {
	events := []eventRow{
		{Kind: "run.failed", SourceRef: "category=provider_protocol"},
		{Kind: "run.failed", SourceRef: "category=provider_protocol"},
	}
	got := ruleProviderProtocolFailures(events)
	if len(got) != 1 {
		t.Fatalf("want 1 memory, got %d", len(got))
	}
	if got[0].Authority != memory.AuthorityInferred {
		t.Fatalf("want authority=%s, got %s", memory.AuthorityInferred, got[0].Authority)
	}
	if got[0].Claim != "Provider 协议层不稳定" {
		t.Fatalf("want claim %q, got %q", "Provider 协议层不稳定", got[0].Claim)
	}
}

// TestRuleProviderProtocolFailuresRequiresTwo confirms a single failure
// stays silent — the threshold is ≥ 2 per spec §4.
func TestRuleProviderProtocolFailuresRequiresTwo(t *testing.T) {
	events := []eventRow{
		{Kind: "run.failed", SourceRef: "category=provider_protocol"},
	}
	if got := ruleProviderProtocolFailures(events); got != nil {
		t.Fatalf("expected no memory for single failure, got %+v", got)
	}
}

// TestRuleProviderProtocolFailuresIgnoresOtherCategories confirms only the
// provider_protocol category drives the rule; other categories do not
// accumulate toward the threshold.
func TestRuleProviderProtocolFailuresIgnoresOtherCategories(t *testing.T) {
	events := []eventRow{
		{Kind: "run.failed", SourceRef: "category=rate_limit"},
		{Kind: "run.failed", SourceRef: "category=timeout"},
	}
	if got := ruleProviderProtocolFailures(events); got != nil {
		t.Fatalf("expected no memory for non-provider failures, got %+v", got)
	}
}

// TestRulesExtractPlaceholder documents the Task 3 contract for
// Rules.Extract: until Task 4 wires the real event source, the method
// returns (nil, nil) without error. The nil/nil shape lets app.Runtime
// short-circuit cleanly while the extractor is being developed.
func TestRulesExtractPlaceholder(t *testing.T) {
	rules := NewRules()
	got, err := rules.Extract(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil memories (Task 3 placeholder), got %+v", got)
	}
}

// TestRulesAllRulesRegistered confirms each of the six rule functions from
// spec §4 is wired into Rules.allRules. A new rule added to spec §4 must
// be added here too; otherwise this list-driven test will start failing.
func TestRulesAllRulesRegistered(t *testing.T) {
	rules := NewRules()
	all := rules.allRules()
	if len(all) != 6 {
		t.Fatalf("want 6 rules registered, got %d", len(all))
	}
}