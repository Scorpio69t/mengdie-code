// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Scorpio69t/mengdie-code/internal/provider"
)

func TestReadContextSourceReturnsBoundedVerifiedPages(t *testing.T) {
	env := newToolTestEnv(t)
	reader := &fakeContextSourceReader{
		descriptor: ContextSourceDescriptor{SummarySHA256: "sha256:summary-one", SourceStart: 2, SourceEnd: 4},
		messages: []ContextSourceMessage{
			contextSourceTestMessage(2, provider.RoleAssistant, "旧结论"),
			contextSourceTestMessage(3, provider.RoleTool, "恢复安全工具结果"),
			contextSourceTestMessage(4, provider.RoleAssistant, "后续结论"),
		},
	}
	tool, err := NewReadContextSource(reader)
	if err != nil {
		t.Fatal(err)
	}
	call := prepareCall(t, tool, env, `{"offset":0,"limit":2}`)
	if strings.Contains(string(call.CanonicalArg), "session") || !strings.Contains(string(call.CanonicalArg), reader.descriptor.SummarySHA256) {
		t.Fatalf("canonical args leaked identity or missed summary binding: %s", call.CanonicalArg)
	}
	result := executeCall(t, tool, env, call)
	if len(result.Output) > maxContextSourceOutput || !result.Truncated {
		t.Fatalf("result bytes=%d truncated=%v", len(result.Output), result.Truncated)
	}
	var page contextSourcePage
	if err := json.Unmarshal([]byte(result.Output), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 2 || page.Entries[0].Ordinal != 2 || !page.Entries[0].Complete ||
		page.NextOffset != 2 || page.NextByte != 0 || page.SummarySHA256 != reader.descriptor.SummarySHA256 {
		t.Fatalf("page=%+v", page)
	}
	var recovered provider.Message
	if err := json.Unmarshal([]byte(page.Entries[0].MessageJSON), &recovered); err != nil {
		t.Fatal(err)
	}
	if recovered.Content != "旧结论" || recovered.Role != provider.RoleAssistant {
		t.Fatalf("recovered=%+v", recovered)
	}
}

func TestReadContextSourceChunksOversizedMessageAndContinues(t *testing.T) {
	env := newToolTestEnv(t)
	reader := &fakeContextSourceReader{
		descriptor: ContextSourceDescriptor{SummarySHA256: "sha256:summary-large", SourceStart: 2, SourceEnd: 2},
		messages:   []ContextSourceMessage{contextSourceTestMessage(2, provider.RoleTool, strings.Repeat("引号\\\"", 4096))},
	}
	tool, err := NewReadContextSource(reader)
	if err != nil {
		t.Fatal(err)
	}
	first := executeCall(t, tool, env, prepareCall(t, tool, env, `{"offset":0,"limit":1}`))
	if len(first.Output) > maxContextSourceOutput {
		t.Fatalf("first output bytes=%d", len(first.Output))
	}
	var page contextSourcePage
	if err := json.Unmarshal([]byte(first.Output), &page); err != nil {
		t.Fatal(err)
	}
	if !page.HasMore || page.NextOffset != 0 || page.NextByte <= 0 || page.Entries[0].Complete {
		t.Fatalf("first page=%+v", page)
	}
	raw, err := json.Marshal(readContextSourceArgs{Offset: page.NextOffset, Limit: 1, ByteOffset: page.NextByte})
	if err != nil {
		t.Fatal(err)
	}
	second := executeCall(t, tool, env, prepareCall(t, tool, env, string(raw)))
	var continued contextSourcePage
	if err := json.Unmarshal([]byte(second.Output), &continued); err != nil {
		t.Fatal(err)
	}
	if continued.Entries[0].ByteStart != page.NextByte || continued.Entries[0].MessageJSON == "" {
		t.Fatalf("continued=%+v", continued)
	}
}

func TestReadContextSourceRejectsSummaryRotationAndInvalidRequests(t *testing.T) {
	env := newToolTestEnv(t)
	reader := &fakeContextSourceReader{
		descriptor: ContextSourceDescriptor{SummarySHA256: "sha256:summary-old", SourceStart: 2, SourceEnd: 2},
		messages:   []ContextSourceMessage{contextSourceTestMessage(2, provider.RoleAssistant, "事实")},
	}
	tool, err := NewReadContextSource(reader)
	if err != nil {
		t.Fatal(err)
	}
	call := prepareCall(t, tool, env, `{"offset":0,"limit":1}`)
	reader.descriptor.SummarySHA256 = "sha256:summary-new"
	if _, err := tool.Execute(context.Background(), call, capabilityFor(call), env.execEnv()); !errors.Is(err, errFakeContextSourceChanged) {
		t.Fatalf("Execute() error=%v", err)
	}
	for _, raw := range []string{`{"offset":1}`, `{"offset":0,"limit":5}`, `{"offset":0,"byte_offset":1}`} {
		if _, err := tool.Prepare(context.Background(), json.RawMessage(raw), env.prepareEnv()); err == nil {
			t.Fatalf("Prepare(%s) succeeded", raw)
		}
	}
}

func TestReadContextSourceFailsClosedWithoutSummaryAndOnCancellation(t *testing.T) {
	env := newToolTestEnv(t)
	reader := &fakeContextSourceReader{describeErr: errors.New("summary missing")}
	tool, err := NewReadContextSource(reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Prepare(context.Background(), json.RawMessage(`{"offset":0}`), env.prepareEnv()); err == nil {
		t.Fatal("Prepare() succeeded without summary")
	}
	reader.describeErr = nil
	reader.descriptor = ContextSourceDescriptor{SummarySHA256: "sha256:summary", SourceStart: 2, SourceEnd: 2}
	reader.messages = []ContextSourceMessage{contextSourceTestMessage(2, provider.RoleAssistant, "事实")}
	call := prepareCall(t, tool, env, `{"offset":0,"limit":1}`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := tool.Execute(ctx, call, capabilityFor(call), env.execEnv()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Execute() error=%v", err)
	}
}

var errFakeContextSourceChanged = errors.New("summary changed")

type fakeContextSourceReader struct {
	descriptor  ContextSourceDescriptor
	messages    []ContextSourceMessage
	describeErr error
}

func (r *fakeContextSourceReader) Describe(ctx context.Context) (ContextSourceDescriptor, error) {
	if err := ctx.Err(); err != nil {
		return ContextSourceDescriptor{}, err
	}
	return r.descriptor, r.describeErr
}

func (r *fakeContextSourceReader) Load(ctx context.Context, expected ContextSourceDescriptor) ([]ContextSourceMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if expected != r.descriptor {
		return nil, errFakeContextSourceChanged
	}
	return append([]ContextSourceMessage(nil), r.messages...), nil
}

func contextSourceTestMessage(ordinal uint64, role provider.Role, content string) ContextSourceMessage {
	return ContextSourceMessage{
		Ordinal: ordinal, Role: role, Completeness: "full",
		SHA256: "sha256:message", Message: provider.Message{Role: role, Content: content},
	}
}
