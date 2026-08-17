// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

// Package cost provides deterministic, local estimates for model usage. It
// deliberately matches an endpoint origin and an exact model name: aliases and
// unknown gateways never inherit a price accidentally.
package cost

import (
	"fmt"
	"math"
	"net/url"
	"strings"

	"github.com/Scorpio69t/mengdie-code/internal/provider"
)

const (
	// TableVersion changes whenever a price or supported exact model changes.
	TableVersion = "2026-08-17"
	CurrencyUSD  = "USD"

	StatusEstimated = "estimated"
	StatusUnknown   = "unknown"

	UnknownUsageUnreported  = "usage_unreported"
	UnknownPriceUnavailable = "price_unavailable"
	UnknownInvalidUsage     = "invalid_usage"
	UnknownCostOverflow     = "cost_overflow"
)

const deepSeekPricingSource = "https://api-docs.deepseek.com/quick_start/pricing"

type rate struct {
	origin                string
	model                 string
	inputMissPicoPerToken int64
	cacheReadPicoPerToken int64
	outputPicoPerToken    int64
	source                string
}

// Rates are integer pico-USD per token, derived from the official per-million
// token prices. Integer arithmetic avoids presenting floating-point rounding as
// a billing fact.
var rates = []rate{
	{
		origin: "https://api.deepseek.com", model: "deepseek-v4-flash",
		inputMissPicoPerToken: 140_000, cacheReadPicoPerToken: 2_800,
		outputPicoPerToken: 280_000, source: deepSeekPricingSource,
	},
	{
		origin: "https://api.deepseek.com", model: "deepseek-v4-pro",
		inputMissPicoPerToken: 435_000, cacheReadPicoPerToken: 3_625,
		outputPicoPerToken: 870_000, source: deepSeekPricingSource,
	},
}

type Estimator struct {
	origin string
	model  string
	rate   *rate
}

type Estimate struct {
	ProviderOrigin string
	Model          string
	Status         string
	PicoUSD        int64
	Currency       string
	TableVersion   string
	PricingSource  string
	UnknownReason  string
}

func NewEstimator(baseURL, model string) Estimator {
	origin := Origin(baseURL)
	model = strings.TrimSpace(model)
	estimator := Estimator{origin: origin, model: model}
	for index := range rates {
		candidate := &rates[index]
		if candidate.origin == origin && candidate.model == model {
			estimator.rate = candidate
			break
		}
	}
	return estimator
}

// Origin removes paths, queries, fragments, and user information before a
// provider endpoint can cross the public-fact boundary.
func Origin(baseURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host)
}

func (e Estimator) Estimate(usage provider.Usage, reported bool) Estimate {
	return e.EstimateForModel(usage, reported, e.model)
}

func (e Estimator) EstimateForModel(usage provider.Usage, reported bool, model string) Estimate {
	model = strings.TrimSpace(model)
	result := Estimate{
		ProviderOrigin: e.origin, Model: model, Status: StatusUnknown,
		TableVersion: TableVersion,
	}
	if !reported {
		result.UnknownReason = UnknownUsageUnreported
		return result
	}
	if usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.TotalTokens < 0 || usage.CacheReadTokens < 0 ||
		usage.CacheReadTokens > usage.InputTokens {
		result.UnknownReason = UnknownInvalidUsage
		return result
	}
	if e.rate == nil || model != e.model {
		result.UnknownReason = UnknownPriceUnavailable
		return result
	}
	inputMiss := usage.InputTokens - usage.CacheReadTokens
	amount, ok := multiplyAdd(0, inputMiss, e.rate.inputMissPicoPerToken)
	if ok {
		amount, ok = multiplyAdd(amount, usage.CacheReadTokens, e.rate.cacheReadPicoPerToken)
	}
	if ok {
		amount, ok = multiplyAdd(amount, usage.OutputTokens, e.rate.outputPicoPerToken)
	}
	if !ok {
		result.UnknownReason = UnknownCostOverflow
		return result
	}
	result.Status = StatusEstimated
	result.PicoUSD = amount
	result.Currency = CurrencyUSD
	result.PricingSource = e.rate.source
	return result
}

func multiplyAdd(total, count, price int64) (int64, bool) {
	if count < 0 || price < 0 || (count != 0 && price > math.MaxInt64/count) {
		return 0, false
	}
	value := count * price
	if total > math.MaxInt64-value {
		return 0, false
	}
	return total + value, true
}

// FormatPicoUSD renders an exact pico-USD integer without floating point.
func FormatPicoUSD(value int64) string {
	whole := value / 1_000_000_000_000
	fraction := value % 1_000_000_000_000
	if fraction == 0 {
		return fmt.Sprintf("%d", whole)
	}
	return fmt.Sprintf("%d.%s", whole, strings.TrimRight(fmt.Sprintf("%012d", fraction), "0"))
}
