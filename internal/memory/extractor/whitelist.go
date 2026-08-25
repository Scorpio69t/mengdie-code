// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

// Package extractor 把已完成的 Agent Run 转成候选 memory 行。本文件
// 集中维护 v0.1 的 fingerprint whitelist：Hybrid 抽取管线在
// ProposeMemory 后，用 ShouldAutoApprove 判定是否直接把候选升级到
// Approved 状态。命中任何 fingerprint 模式的 claim 视作"结构信号"
// （如工具名、命令行、显式偏好），系统信任度足以跳过用户复核；
// 未命中的候选仍保持 Proposed，等待用户在 CLI 或 Daemon UI 中 review。
//
// fingerprintPatterns 是 v0.1 的静态白名单；v0.2 可能改为从 config
// 加载或基于 accept frequency 在线学习，但目前保持固定以便评测。
package extractor

import "strings"

// FingerprintPattern returns true if the given claim matches the pattern.
type FingerprintPattern func(claim string) bool

// fingerprintPatterns is the package-level whitelist checked by
// ShouldAutoApprove. Order matters only for diagnostics; ShouldAutoApprove
// returns true on the first match so any ordering is functionally
// equivalent.
var fingerprintPatterns = []FingerprintPattern{
	isProjectUsesEditFile,
	isProjectUsesWriteFile,
	isProjectTestEntrance,
	isProjectUsesGolangciLint,
	isRunOverallSuccess,
	isProviderUnstable,
	isPrefersChineseREADME,
	isPrefersStderrFirst,
}

// 6 Rules-related fingerprint patterns (spec §4.1) cover deterministic
// signals Rules already emits; if a Rules claim matches the same
// substring we skip the user-review step for that line.

func isProjectUsesEditFile(c string) bool  { return strings.Contains(c, "edit_file") }
func isProjectUsesWriteFile(c string) bool { return strings.Contains(c, "write_file") }
func isProjectTestEntrance(c string) bool  { return strings.Contains(c, "go test") }
func isProjectUsesGolangciLint(c string) bool {
	return strings.Contains(c, "golangci-lint")
}
func isRunOverallSuccess(c string) bool {
	return strings.Contains(c, "本次 Agent Run 整体成功")
}
func isProviderUnstable(c string) bool {
	return strings.Contains(c, "Provider 协议层不稳定")
}

// 2 LLM-fingerprint patterns cover claims the LLM extractor emits about
// user-visible preferences that the Rules pipeline cannot observe from
// session events alone.

func isPrefersChineseREADME(c string) bool { return strings.Contains(c, "中文 README") }
func isPrefersStderrFirst(c string) bool   { return strings.Contains(c, "stderr") }

// ShouldAutoApprove returns true if any fingerprint pattern matches the
// claim. Hybrid extraction calls this on every candidate memory row; a
// true result lets app.Runtime short-circuit ProposeMemory → Approve
// without waiting for the user, false leaves the row as Proposed.
func ShouldAutoApprove(claim string) bool {
	for _, p := range fingerprintPatterns {
		if p(claim) {
			return true
		}
	}
	return false
}
