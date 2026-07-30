// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

// Package brand owns the product identity rendered by terminal clients.
package brand

import (
	"fmt"
	"io"
)

// Mark is the compact terminal representation of the MengDie butterfly.
const Mark = `  ╭╲      ╱╮
  │ ╲    ╱ │
  ╰╮ ╲  ╱ ╭╯
    ╲ ╲╱ ╱
    ╱ ╱╲ ╲
  ╭╯ ╱  ╲ ╰╮
  │ ╱    ╲ │
  ╰╱      ╲╯`

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

// WriteWelcome renders a stable, color-independent welcome screen.
// Color is intentionally left to the future terminal renderer so redirected
// output and accessibility modes never receive embedded escape sequences.
func WriteWelcome(w io.Writer, info Info) error {
	_, err := fmt.Fprintf(w, `%s

MengDie Code / 梦蝶 Code  %s
不是记得更多，而是记得更对。

  构建  %s · %s
  平台  %s · %s
  项目  %s
  模型  %s
  安全  %s

`, Mark, info.Version, info.Commit, info.BuildDate, info.Platform, info.GoVersion, info.WorkDir, info.Model, info.Security)
	return err
}
