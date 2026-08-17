// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package terminal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/Scorpio69t/mengdie-code/internal/cost"
	"github.com/Scorpio69t/mengdie-code/internal/events"
)

// HumanRenderer writes stable, color-independent Chinese output. It only
// decodes allowlisted payload fields and never dumps an unknown raw payload.
type HumanRenderer struct {
	mu       sync.Mutex
	writer   io.Writer
	streamed map[string]bool
}

func NewHumanRenderer(writer io.Writer) (*HumanRenderer, error) {
	if writer == nil {
		return nil, errors.New("human renderer writer is required")
	}
	return &HumanRenderer{writer: writer, streamed: make(map[string]bool)}, nil
}

func (r *HumanRenderer) Emit(ctx context.Context, event events.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := event.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.render(event)
}

func (r *HumanRenderer) render(event events.Event) error {
	switch event.Kind {
	case events.KindRunStarted:
		payload, err := events.DecodePayload[events.RunStarted](event)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintln(r.writer, "开始任务"); err != nil {
			return err
		}
		if payload.Model != "" {
			if _, err := fmt.Fprintf(r.writer, "• 模型：%s\n", payload.Model); err != nil {
				return err
			}
		}
		return nil
	case events.KindMessageDelta:
		payload, err := events.DecodePayload[events.MessageDelta](event)
		if err != nil {
			return err
		}
		r.streamed[event.RunID] = true
		_, err = io.WriteString(r.writer, payload.Text)
		return err
	case events.KindMessageCompleted:
		payload, err := events.DecodePayload[events.MessageCompleted](event)
		if err != nil {
			return err
		}
		if r.streamed[event.RunID] {
			delete(r.streamed, event.RunID)
			_, err = fmt.Fprintln(r.writer)
			return err
		}
		_, err = fmt.Fprintln(r.writer, payload.Text)
		return err
	case events.KindTodoUpdated:
		payload, err := events.DecodePayload[events.TodoUpdated](event)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintln(r.writer, "计划"); err != nil {
			return err
		}
		for i, todo := range payload.Todos {
			if _, err := fmt.Fprintf(r.writer, "  %d. [%s] %s\n", i+1, todoStatus(todo.Status), todo.Content); err != nil {
				return err
			}
		}
		return nil
	case events.KindToolProposed:
		payload, err := events.DecodePayload[events.ToolProposed](event)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(r.writer, "准备 %s%s\n", payload.Tool, prefixed(payload.Summary, "："))
		return err
	case events.KindApprovalNeeded:
		payload, err := events.DecodePayload[events.ApprovalNeeded](event)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(r.writer, "需要批准%s：%s\n", bracketed(payload.Risk), payload.Prompt)
		return err
	case events.KindApprovalResolved:
		payload, err := events.DecodePayload[events.ApprovalResolved](event)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(r.writer, "审批：%s%s\n", payload.Decision, prefixed(payload.Reason, " · "))
		return err
	case events.KindToolStarted:
		payload, err := events.DecodePayload[events.ToolStarted](event)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(r.writer, "执行 %s\n", payload.Tool)
		return err
	case events.KindToolCompleted:
		payload, err := events.DecodePayload[events.ToolCompleted](event)
		if err != nil {
			return err
		}
		mark := "✗"
		if payload.Success {
			mark = "✓"
		}
		_, err = fmt.Fprintf(r.writer, "%s %s%s\n", mark, payload.Tool, prefixed(payload.Summary, "："))
		return err
	case events.KindUsageUpdated:
		payload, err := events.DecodePayload[events.UsageUpdated](event)
		if err != nil {
			return err
		}
		if payload.RequestCount == 0 {
			_, err = fmt.Fprintf(r.writer, "• 用量：输入 %d · 输出 %d · 总计 %d · 缓存读取 %d tokens\n", payload.InputTokens, payload.OutputTokens, payload.TotalTokens, payload.CacheReadTokens)
			return err
		}
		costText := fmt.Sprintf("成本未知（%s）", usageUnknownReason(payload.CostUnknownReason))
		if payload.CostStatus == cost.StatusEstimated {
			costText = fmt.Sprintf("估算成本 $%s USD（价表 %s）", cost.FormatPicoUSD(payload.EstimatedCostPicoUSD), payload.PriceTableVersion)
		}
		_, err = fmt.Fprintf(r.writer, "• 用量（%s）：请求 %d · 输入 %d · 输出 %d · 总计 %d · 缓存读取 %d tokens · %s\n",
			usagePurpose(payload.Purpose), payload.RequestCount, payload.InputTokens, payload.OutputTokens, payload.TotalTokens, payload.CacheReadTokens, costText)
		return err
	case events.KindWarning:
		payload, err := events.DecodePayload[events.Warning](event)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(r.writer, "! %s%s\n", payload.Message, prefixed(payload.Code, " · "))
		return err
	case events.KindRunCompleted:
		payload, err := events.DecodePayload[events.RunCompleted](event)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(r.writer, "✓ 任务完成%s\n", prefixed(payload.Summary, "："))
		return err
	case events.KindRunFailed:
		payload, err := events.DecodePayload[events.RunFailed](event)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(r.writer, "✗ 任务失败%s：%s\n", bracketed(payload.Category), payload.Message)
		return err
	case events.KindRunCancelled:
		payload, err := events.DecodePayload[events.RunCancelled](event)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(r.writer, "已取消%s\n", prefixed(payload.Reason, "："))
		return err
	default:
		_, err := fmt.Fprintf(r.writer, "• %s\n", event.Kind)
		return err
	}
}

func usagePurpose(purpose string) string {
	if purpose == "context_summary" {
		return "上下文摘要"
	}
	return "Agent"
}

func usageUnknownReason(reason string) string {
	switch reason {
	case cost.UnknownUsageUnreported:
		return "Provider 未上报 token"
	case cost.UnknownPriceUnavailable:
		return "无匹配价表"
	case cost.UnknownInvalidUsage:
		return "用量数据无效"
	case cost.UnknownCostOverflow:
		return "估算超出范围"
	default:
		return "原因未记录"
	}
}

func todoStatus(status string) string {
	switch status {
	case "in_progress":
		return "进行中"
	case "completed":
		return "已完成"
	case "cancelled":
		return "已取消"
	case "pending":
		return "待处理"
	default:
		return fallback(status, "未知")
	}
}

func prefixed(value, prefix string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return prefix + value
}

func bracketed(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return " [" + value + "]"
}

func fallback(value, fallbackValue string) string {
	if strings.TrimSpace(value) == "" {
		return fallbackValue
	}
	return value
}
