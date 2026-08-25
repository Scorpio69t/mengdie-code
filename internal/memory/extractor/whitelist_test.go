// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package extractor

import "testing"

// TestShouldAutoApproveEachPattern exercises every fingerprint pattern plus
// two negative cases. The list is the canonical v0.1 whitelist: 8 claims
// that hybrid extraction can auto-Approve (ProposeMemory → Approve) and 2
// claims that must stay proposed awaiting user review.
func TestShouldAutoApproveEachPattern(t *testing.T) {
	cases := []struct {
		claim string
		want  bool
		name  string
	}{
		{"项目使用 edit_file 修改文件", true, "edit_file"},
		{"项目使用 write_file 创建或覆盖文件", true, "write_file"},
		{"项目测试入口是 go test ./...", true, "go_test"},
		{"项目使用 golangci-lint 做静态检查", true, "golangci_lint"},
		{"本次 Agent Run 整体成功", true, "run_success"},
		{"Provider 协议层不稳定", true, "provider_unstable"},
		{"项目偏好中文 README", true, "chinese_readme"},
		{"项目偏好 stderr 优先", true, "stderr_first"},
		{"用户偏好每次都跑 npm test", false, "non_fingerprint"},
		{"", false, "empty"},
	}
	for _, c := range cases {
		if got := ShouldAutoApprove(c.claim); got != c.want {
			t.Errorf("%s: ShouldAutoApprove(%q) = %v, want %v", c.name, c.claim, got, c.want)
		}
	}
}
