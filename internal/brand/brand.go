// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

// Package brand owns the product identity rendered by terminal clients.
package brand

import (
	"io"
	"strings"
	"unicode/utf8"
)

// Mark is the terminal counterpart of assets/brand/mengdie-mark.svg. Two
// angle-bracket wings surround a split insertion caret: code on both sides,
// with an evidence boundary in the middle. It intentionally uses line art so
// it remains crisp in macOS and Windows terminals without half-block shading.
const Mark = `╲╲      ╱╱
 ╲╲  ╷ ╱╱
  ╲╲ │╱╱
  ╱╱ │╲╲
 ╱╱  ╵ ╲╲
╱╱      ╲╲`

// CompactMark is used in dense headers where the full six-line mark would
// compete with the conversation.
const CompactMark = "╲╱│╲╱"

// Info contains the runtime facts shown on the interactive welcome screen.
// Values must already be safe for display; secrets never belong here.
type Info struct {
	Version   string
	Commit    string
	BuildDate string
	GoVersion string
	Platform  string
	WorkDir   string
	Model     string
	Security  string
}

// WriteWelcome renders a stable, color-independent welcome screen with the
// mark on the left and runtime facts on the right. Color is intentionally
// left to the future terminal renderer so redirected output and
// accessibility modes never receive embedded escape sequences.
func WriteWelcome(w io.Writer, info Info) error {
	mark := strings.Split(Mark, "\n")
	markWidth := 0
	for _, line := range mark {
		if n := utf8.RuneCountInString(line); n > markWidth {
			markWidth = n
		}
	}

	facts := []string{
		"MengDie Code / 梦蝶 Code  " + info.Version,
		"不是记得更多，而是记得更对。",
		"",
		"构建  " + info.Commit + " · " + info.BuildDate,
		"平台  " + info.Platform + " · " + info.GoVersion,
		"项目  " + info.WorkDir,
		"模型  " + info.Model,
		"安全  " + info.Security,
	}

	var b strings.Builder
	for i := 0; i < max(len(mark), len(facts)); i++ {
		var left string
		if i < len(mark) {
			left = mark[i]
			b.WriteString(left)
		}
		if i < len(facts) && facts[i] != "" {
			b.WriteString(strings.Repeat(" ", markWidth-utf8.RuneCountInString(left)+3))
			b.WriteString(facts[i])
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")

	_, err := io.WriteString(w, b.String())
	return err
}
