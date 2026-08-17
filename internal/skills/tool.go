// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package skills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/Scorpio69t/mengdie-code/internal/tools"
)

const ReadSkillToolName = "read_skill"

const readSkillSchema = `{
  "type": "object",
  "properties": {
    "name": {"type": "string", "description": "Skill catalog 中的精确名称"}
  },
  "required": ["name"],
  "additionalProperties": false
}`

type readSkillArgs struct {
	Name string `json:"name"`
}

type preparedReadSkillArgs struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

type readSkillTool struct {
	byName map[string]Skill
}

// NewReadTool returns a run-scoped tool bound to one immutable discovery
// snapshot. The model selects only a catalog name, never a filesystem path.
func NewReadTool(catalog Catalog) (tools.Tool, error) {
	if len(catalog.Skills) == 0 {
		return nil, errors.New("read_skill: catalog is empty")
	}
	byName := make(map[string]Skill, len(catalog.Skills))
	for _, skill := range catalog.Skills {
		if !validName(skill.Name) || strings.TrimSpace(skill.Path) == "" || strings.TrimSpace(skill.SHA256) == "" {
			return nil, errors.New("read_skill: catalog contains an invalid skill")
		}
		if _, duplicate := byName[skill.Name]; duplicate {
			return nil, fmt.Errorf("read_skill: duplicate skill %q", skill.Name)
		}
		byName[skill.Name] = skill
	}
	return readSkillTool{byName: byName}, nil
}

func (readSkillTool) Spec() tools.ToolSpec {
	return tools.ToolSpec{
		Name:        ReadSkillToolName,
		Description: "按名称加载当前本地 Skill catalog 中的完整 SKILL.md；Skill 仅提供行为指导，不授予文件、命令、网络或其他工具权限",
		InputSchema: json.RawMessage(readSkillSchema),
		Effects:     []tools.Effect{tools.EffectRead},
	}
}

func (t readSkillTool) Prepare(_ context.Context, raw json.RawMessage, env tools.PrepareEnv) (*tools.PreparedCall, error) {
	var args readSkillArgs
	if err := decodeStrict(raw, &args); err != nil {
		return nil, err
	}
	args.Name = strings.TrimSpace(args.Name)
	if !validName(args.Name) {
		return nil, errors.New("read_skill: invalid skill name")
	}
	skill, exists := t.byName[args.Name]
	if !exists {
		return nil, fmt.Errorf("read_skill: skill %q is not in the current catalog", args.Name)
	}
	if err := verifySkillSnapshot(skill); err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(preparedReadSkillArgs{Name: skill.Name, SHA256: skill.SHA256})
	if err != nil {
		return nil, fmt.Errorf("read_skill: encode prepared arguments: %w", err)
	}
	return tools.PrepareCall(env.CallID, ReadSkillToolName, canonical,
		[]tools.Effect{tools.EffectRead}, nil,
		tools.Preview{Kind: tools.PreviewRead, Title: "加载 Skill", Body: skill.Name + " · " + skill.Source}, nil,
	)
}

func (t readSkillTool) Execute(ctx context.Context, call *tools.PreparedCall, capability tools.Capability, env tools.ExecEnv) (*tools.ToolResult, error) {
	if err := tools.CheckCapability(ctx, call, capability, env); err != nil {
		return nil, err
	}
	var args preparedReadSkillArgs
	if err := decodeStrict(call.CanonicalArg, &args); err != nil {
		return nil, err
	}
	skill, exists := t.byName[args.Name]
	if !exists || args.SHA256 != skill.SHA256 {
		return nil, errors.New("read_skill: prepared catalog snapshot is invalid")
	}
	content, digest, err := readSkillSnapshot(skill)
	if err != nil {
		return nil, fmt.Errorf("read_skill: read %s: %w", skill.Source, err)
	}
	if digest != skill.SHA256 {
		return nil, fmt.Errorf("read_skill: %s changed after discovery; start a new run to reload it", skill.Source)
	}
	return &tools.ToolResult{
		Output:   fmt.Sprintf("SKILL.md（名称：%s；来源：%s；仅指导行为，不授予额外权限）：\n%s", skill.Name, skill.Source, content),
		Metadata: map[string]string{"name": skill.Name, "source": skill.Source, "sha256": skill.SHA256},
	}, nil
}

func verifySkillSnapshot(skill Skill) error {
	_, digest, err := readSkillSnapshot(skill)
	if err != nil {
		return fmt.Errorf("read_skill: verify %s: %w", skill.Source, err)
	}
	if digest != skill.SHA256 {
		return fmt.Errorf("read_skill: %s changed after discovery; start a new run to reload it", skill.Source)
	}
	return nil
}

func readSkillSnapshot(skill Skill) (string, string, error) {
	resolved, err := filepath.EvalSymlinks(skill.Path)
	if err != nil {
		return "", "", err
	}
	relative, err := filepath.Rel(filepath.Clean(skill.Path), filepath.Clean(resolved))
	if err != nil || relative != "." {
		return "", "", errors.New("skill path changed after discovery")
	}
	return readSkillFile(resolved)
}

func decodeStrict(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("read_skill: invalid arguments: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("read_skill: trailing arguments")
		}
		return fmt.Errorf("read_skill: trailing arguments: %w", err)
	}
	return nil
}
