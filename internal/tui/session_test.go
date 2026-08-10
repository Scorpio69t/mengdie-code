// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Scorpio69t/mengdie-code/internal/events"
	"github.com/Scorpio69t/mengdie-code/internal/session"
)

func TestRenderSessionUsesOnlyPublicFactsAndHandlesNarrowWidth(t *testing.T) {
	view := session.SessionView{ID: "会话-测试", Status: "interrupted", LastSeq: 8,
		Messages:  []session.MessageView{{Text: "修复中文宽字符"}},
		Tools:     []session.ToolView{{Tool: "read_file", Phase: "completed"}},
		Approvals: []session.ApprovalView{{CallID: "call-1"}},
		Todos:     []events.Todo{{Content: "验证 Windows", Status: "in_progress"}},
	}
	output := RenderSession(view, 100, false)
	for _, want := range []string{"修复中文宽字符", "read_file：completed", "待审批调用 call-1", "验证 Windows", "公开事实视图"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q: %s", want, output)
		}
	}
	if narrow := RenderSession(view, 30, false); !strings.Contains(narrow, "终端过窄") {
		t.Fatalf("narrow output=%q", narrow)
	}
}

func TestSubscribedSessionModelReplaysNotificationGapAndCloses(t *testing.T) {
	subscription := &testFactSubscription{notifications: make(chan session.PublicFactNotification, 2)}
	source := &testFactSource{subscription: subscription, pages: map[uint64]session.PublicFactPage{
		1: {Facts: []session.PublicFact{tuiWarningFact(t, 2, "two")}, ThroughSeq: 2},
		2: {Facts: []session.PublicFact{tuiWarningFact(t, 3, "three"), tuiWarningFact(t, 4, "four")}, ThroughSeq: 4},
	}}
	model := NewSubscribedSessionModel(session.SessionView{ID: "session-1", Status: "active", LastSeq: 1}, 100, false, source)
	started := model.Init()()
	updated, command := model.Update(started)
	model = updated.(SessionModel)
	if model.view.LastSeq != 2 || len(model.view.Warnings) != 1 {
		t.Fatalf("after initial replay=%+v", model.view)
	}
	subscription.notifications <- session.PublicFactNotification{Fact: tuiWarningFact(t, 4, "four"), Gap: true}
	notification := command()
	updated, command = model.Update(notification)
	model = updated.(SessionModel)
	if command == nil {
		t.Fatal("gap did not schedule durable replay")
	}
	updated, _ = model.Update(command())
	model = updated.(SessionModel)
	if model.view.LastSeq != 4 || len(model.view.Warnings) != 3 {
		t.Fatalf("after gap replay=%+v", model.view)
	}
	if len(source.afterSequences) != 2 || source.afterSequences[0] != 1 || source.afterSequences[1] != 2 {
		t.Fatalf("after sequences=%v", source.afterSequences)
	}
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Text: "q", Code: 'q'}))
	model = updated.(SessionModel)
	if !subscription.closed || model.subscription != nil {
		t.Fatalf("subscription closed=%t model subscription=%v", subscription.closed, model.subscription)
	}
}

type testFactSource struct {
	subscription   FactSubscription
	pages          map[uint64]session.PublicFactPage
	afterSequences []uint64
}

func (source *testFactSource) ReplayPublicFacts(_ context.Context, _ string, afterSeq uint64, _ int) (session.PublicFactPage, error) {
	source.afterSequences = append(source.afterSequences, afterSeq)
	return source.pages[afterSeq], nil
}

func (source *testFactSource) SubscribePublicFacts(_ string, _ uint64) (FactSubscription, error) {
	return source.subscription, nil
}

type testFactSubscription struct {
	notifications chan session.PublicFactNotification
	closed        bool
}

func (subscription *testFactSubscription) Notifications() <-chan session.PublicFactNotification {
	return subscription.notifications
}

func (subscription *testFactSubscription) Close() {
	if subscription.closed {
		return
	}
	subscription.closed = true
	close(subscription.notifications)
}

func tuiWarningFact(t *testing.T, sequence uint64, message string) session.PublicFact {
	t.Helper()
	event, err := events.New("run-1", sequence, time.Date(2026, 8, 10, 9, 0, int(sequence), 0, time.UTC), events.KindWarning, events.Warning{Message: message})
	if err != nil {
		t.Fatal(err)
	}
	return session.PublicFact{
		SessionID: "session-1", SessionSeq: sequence, RunID: event.RunID,
		RunSeq: event.Seq, Kind: event.Kind, SchemaVersion: event.Version,
		Payload: event.Payload, Time: event.Time,
	}
}
