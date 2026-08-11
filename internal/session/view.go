// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/Scorpio69t/mengdie-code/internal/events"
)

const SnapshotSchemaVersion uint16 = 1

// SessionView is the public, deterministic projection consumed by CLI and a
// later TUI. It deliberately excludes private command payloads.
type SessionView struct {
	ID              string                  `json:"id"`
	ProjectRoot     string                  `json:"project_root"`
	ProjectIdentity string                  `json:"project_identity"`
	Title           string                  `json:"title,omitempty"`
	Status          string                  `json:"status"`
	LastSeq         uint64                  `json:"last_seq"`
	CreatedAt       time.Time               `json:"created_at"`
	UpdatedAt       time.Time               `json:"updated_at"`
	Runs            []RunView               `json:"runs"`
	Messages        []MessageView           `json:"messages"`
	Todos           []events.Todo           `json:"todos"`
	Approvals       []ApprovalView          `json:"approvals"`
	Tools           []ToolView              `json:"tools"`
	Recoveries      []RecoveryView          `json:"recoveries"`
	Compactions     []ContextCompactionView `json:"context_compactions"`
	Usage           UsageView               `json:"usage"`
	Warnings        []events.Warning        `json:"warnings"`
	LastError       *events.RunFailed       `json:"last_error,omitempty"`
}

type RunView struct {
	ID         string    `json:"id"`
	Status     string    `json:"status"`
	Model      string    `json:"model,omitempty"`
	Security   string    `json:"security,omitempty"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
}

type MessageView struct {
	RunID string    `json:"run_id"`
	Text  string    `json:"text"`
	Time  time.Time `json:"time"`
}

type ApprovalView struct {
	RunID    string `json:"run_id"`
	CallID   string `json:"call_id"`
	Prompt   string `json:"prompt,omitempty"`
	Risk     string `json:"risk,omitempty"`
	Decision string `json:"decision,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

type ToolView struct {
	RunID      string   `json:"run_id"`
	CallID     string   `json:"call_id"`
	Tool       string   `json:"tool"`
	Summary    string   `json:"summary,omitempty"`
	Effects    []string `json:"effects,omitempty"`
	Phase      string   `json:"phase"`
	Success    *bool    `json:"success,omitempty"`
	DurationMS int64    `json:"duration_ms,omitempty"`
}

type RecoveryView struct {
	RunID       string `json:"run_id"`
	SourceRunID string `json:"source_run_id"`
	CallID      string `json:"call_id"`
	Action      string `json:"action"`
	Outcome     string `json:"outcome"`
}

type ContextCompactionView struct {
	RunID                    string `json:"run_id"`
	SourceStart              uint64 `json:"source_start"`
	SourceEnd                uint64 `json:"source_end"`
	EstimatedBefore          int    `json:"estimated_before"`
	EstimatedAfterUpperBound int    `json:"estimated_after_upper_bound"`
	GeneratorModel           string `json:"generator_model"`
	GeneratorVersion         string `json:"generator_version"`
}

type UsageView struct {
	InputTokens     int64 `json:"input_tokens"`
	OutputTokens    int64 `json:"output_tokens"`
	CacheReadTokens int64 `json:"cache_read_tokens"`
}

// Reduce is a pure fold over durable records. Unknown future kinds advance
// LastSeq but otherwise leave the view untouched.
func Reduce(base SessionView, records []Record) (SessionView, error) {
	view := cloneSessionView(base)
	for index, record := range records {
		if view.ID == "" {
			view.ID = record.SessionID
		}
		if record.SessionID != view.ID {
			return SessionView{}, fmt.Errorf("reduce record %d belongs to session %q, want %q", index, record.SessionID, view.ID)
		}
		if record.SessionSeq <= view.LastSeq {
			return SessionView{}, fmt.Errorf("reduce record %d sequence %d is not after %d", index, record.SessionSeq, view.LastSeq)
		}
		view.LastSeq = record.SessionSeq
		if record.Time.After(view.UpdatedAt) {
			view.UpdatedAt = record.Time
		}
		if record.Visibility != VisibilityPublic {
			continue
		}
		if err := reduceRecord(&view, record); err != nil {
			return SessionView{}, fmt.Errorf("reduce %s at sequence %d: %w", record.Kind, record.SessionSeq, err)
		}
	}
	return view, nil
}

func reduceRecord(view *SessionView, record Record) error {
	event := events.Event{
		RunID: record.RunID, Seq: record.RunSeq, Version: record.SchemaVersion,
		Time: record.Time, Kind: events.Kind(record.Kind), Payload: record.Payload,
	}
	switch event.Kind {
	case events.KindRunStarted:
		payload, err := events.DecodePayload[events.RunStarted](event)
		if err != nil {
			return err
		}
		run := ensureRun(view, record.RunID)
		run.Status, run.Model, run.Security, run.StartedAt = "running", payload.Model, payload.Security, record.Time
		view.Status = "active"
	case events.KindMessageCompleted:
		payload, err := events.DecodePayload[events.MessageCompleted](event)
		if err != nil {
			return err
		}
		view.Messages = append(view.Messages, MessageView{RunID: record.RunID, Text: payload.Text, Time: record.Time})
	case events.KindTodoUpdated:
		payload, err := events.DecodePayload[events.TodoUpdated](event)
		if err != nil {
			return err
		}
		view.Todos = append([]events.Todo(nil), payload.Todos...)
	case events.KindToolProposed:
		payload, err := events.DecodePayload[events.ToolProposed](event)
		if err != nil {
			return err
		}
		tool := ensureTool(view, record.RunID, payload.CallID)
		tool.Tool, tool.Summary, tool.Effects, tool.Phase = payload.Tool, payload.Summary, append([]string(nil), payload.Effects...), "proposed"
	case events.KindApprovalNeeded:
		payload, err := events.DecodePayload[events.ApprovalNeeded](event)
		if err != nil {
			return err
		}
		approval := ensureApproval(view, record.RunID, payload.CallID)
		approval.Prompt, approval.Risk = payload.Prompt, payload.Risk
	case events.KindApprovalResolved:
		payload, err := events.DecodePayload[events.ApprovalResolved](event)
		if err != nil {
			return err
		}
		approval := ensureApproval(view, record.RunID, payload.CallID)
		approval.Decision, approval.Reason = payload.Decision, payload.Reason
	case events.KindToolStarted:
		payload, err := events.DecodePayload[events.ToolStarted](event)
		if err != nil {
			return err
		}
		tool := ensureTool(view, record.RunID, payload.CallID)
		tool.Tool, tool.Phase = payload.Tool, "running"
	case events.KindToolCompleted:
		payload, err := events.DecodePayload[events.ToolCompleted](event)
		if err != nil {
			return err
		}
		tool := ensureTool(view, record.RunID, payload.CallID)
		result := payload.Success
		tool.Tool, tool.Summary, tool.Phase, tool.Success, tool.DurationMS = payload.Tool, payload.Summary, "completed", &result, payload.DurationMS
	case events.KindRecoveryResolved:
		payload, err := events.DecodePayload[events.RecoveryResolved](event)
		if err != nil {
			return err
		}
		view.Recoveries = append(view.Recoveries, RecoveryView{
			RunID: record.RunID, SourceRunID: payload.SourceRunID, CallID: payload.CallID,
			Action: payload.Action, Outcome: payload.Outcome,
		})
	case events.KindContextCompacted:
		payload, err := events.DecodePayload[events.ContextCompacted](event)
		if err != nil {
			return err
		}
		if payload.SourceStart == 0 || payload.SourceEnd < payload.SourceStart ||
			payload.EstimatedBefore <= 0 || payload.EstimatedAfterUpperBound <= 0 ||
			payload.GeneratorModel == "" || payload.GeneratorVersion == "" {
			return errors.New("invalid context compaction metadata")
		}
		view.Compactions = append(view.Compactions, ContextCompactionView{
			RunID: record.RunID, SourceStart: payload.SourceStart, SourceEnd: payload.SourceEnd,
			EstimatedBefore:          payload.EstimatedBefore,
			EstimatedAfterUpperBound: payload.EstimatedAfterUpperBound,
			GeneratorModel:           payload.GeneratorModel,
			GeneratorVersion:         payload.GeneratorVersion,
		})
	case events.KindUsageUpdated:
		payload, err := events.DecodePayload[events.UsageUpdated](event)
		if err != nil {
			return err
		}
		view.Usage.InputTokens += payload.InputTokens
		view.Usage.OutputTokens += payload.OutputTokens
		view.Usage.CacheReadTokens += payload.CacheReadTokens
	case events.KindWarning:
		payload, err := events.DecodePayload[events.Warning](event)
		if err != nil {
			return err
		}
		view.Warnings = append(view.Warnings, payload)
	case events.KindRunCompleted:
		if _, err := events.DecodePayload[events.RunCompleted](event); err != nil {
			return err
		}
		finishRun(view, record.RunID, "completed", record.Time)
		view.Status = "completed"
	case events.KindRunFailed:
		payload, err := events.DecodePayload[events.RunFailed](event)
		if err != nil {
			return err
		}
		finishRun(view, record.RunID, "failed", record.Time)
		view.Status, view.LastError = "failed", &payload
	case events.KindRunCancelled:
		if _, err := events.DecodePayload[events.RunCancelled](event); err != nil {
			return err
		}
		finishRun(view, record.RunID, "cancelled", record.Time)
		view.Status = "cancelled"
	case events.KindMessageDelta:
		// Deltas are normally transient; tolerate imported logs deterministically.
		if _, err := events.DecodePayload[events.MessageDelta](event); err != nil {
			return err
		}
	default:
		return nil
	}
	return nil
}

func ensureRun(view *SessionView, id string) *RunView {
	for index := range view.Runs {
		if view.Runs[index].ID == id {
			return &view.Runs[index]
		}
	}
	view.Runs = append(view.Runs, RunView{ID: id})
	return &view.Runs[len(view.Runs)-1]
}

func finishRun(view *SessionView, id, status string, at time.Time) {
	run := ensureRun(view, id)
	run.Status, run.FinishedAt = status, at
}

func ensureTool(view *SessionView, runID, callID string) *ToolView {
	for index := range view.Tools {
		if view.Tools[index].RunID == runID && view.Tools[index].CallID == callID {
			return &view.Tools[index]
		}
	}
	view.Tools = append(view.Tools, ToolView{RunID: runID, CallID: callID})
	return &view.Tools[len(view.Tools)-1]
}

func ensureApproval(view *SessionView, runID, callID string) *ApprovalView {
	for index := range view.Approvals {
		if view.Approvals[index].RunID == runID && view.Approvals[index].CallID == callID {
			return &view.Approvals[index]
		}
	}
	view.Approvals = append(view.Approvals, ApprovalView{RunID: runID, CallID: callID})
	return &view.Approvals[len(view.Approvals)-1]
}

func cloneSessionView(view SessionView) SessionView {
	encoded, err := json.Marshal(view)
	if err != nil {
		panic(err)
	}
	var clone SessionView
	if err := json.Unmarshal(encoded, &clone); err != nil {
		panic(err)
	}
	return clone
}

func normalizeSessionView(view *SessionView) {
	if view.Runs == nil {
		view.Runs = []RunView{}
	}
	if view.Messages == nil {
		view.Messages = []MessageView{}
	}
	if view.Todos == nil {
		view.Todos = []events.Todo{}
	}
	if view.Approvals == nil {
		view.Approvals = []ApprovalView{}
	}
	if view.Tools == nil {
		view.Tools = []ToolView{}
	}
	if view.Recoveries == nil {
		view.Recoveries = []RecoveryView{}
	}
	if view.Warnings == nil {
		view.Warnings = []events.Warning{}
	}
	sort.SliceStable(view.Runs, func(i, j int) bool { return view.Runs[i].StartedAt.Before(view.Runs[j].StartedAt) })
}
