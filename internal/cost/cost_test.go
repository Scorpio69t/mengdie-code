// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package cost

import (
	"math"
	"testing"

	"github.com/Scorpio69t/mengdie-code/internal/provider"
)

func TestEstimatorMatchesOriginAndExactModel(t *testing.T) {
	estimator := NewEstimator("https://api.deepseek.com/v1?secret=hidden", "deepseek-v4-flash")
	estimate := estimator.Estimate(provider.Usage{
		InputTokens: 1_000_000, CacheReadTokens: 250_000, OutputTokens: 100_000,
	}, true)
	if estimate.Status != StatusEstimated || estimate.PicoUSD != 133_700_000_000 || estimate.Currency != CurrencyUSD {
		t.Fatalf("estimate=%+v", estimate)
	}
	if estimate.ProviderOrigin != "https://api.deepseek.com" || estimate.Model != "deepseek-v4-flash" ||
		estimate.TableVersion != TableVersion || estimate.PricingSource != deepSeekPricingSource {
		t.Fatalf("metadata=%+v", estimate)
	}
}

func TestEstimatorMakesUnknownStatesExplicit(t *testing.T) {
	tests := []struct {
		name      string
		estimator Estimator
		usage     provider.Usage
		reported  bool
		want      string
	}{
		{name: "unreported", estimator: NewEstimator("https://api.deepseek.com", "deepseek-v4-pro"), want: UnknownUsageUnreported},
		{name: "unknown exact model", estimator: NewEstimator("https://api.deepseek.com", "deepseek-chat"), reported: true, want: UnknownPriceUnavailable},
		{name: "unknown gateway", estimator: NewEstimator("https://gateway.example/v1", "deepseek-v4-pro"), reported: true, want: UnknownPriceUnavailable},
		{name: "invalid cache", estimator: NewEstimator("https://api.deepseek.com", "deepseek-v4-pro"), usage: provider.Usage{InputTokens: 1, CacheReadTokens: 2}, reported: true, want: UnknownInvalidUsage},
		{name: "overflow", estimator: NewEstimator("https://api.deepseek.com", "deepseek-v4-pro"), usage: provider.Usage{OutputTokens: math.MaxInt64}, reported: true, want: UnknownCostOverflow},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := test.estimator.Estimate(test.usage, test.reported)
			if got.Status != StatusUnknown || got.UnknownReason != test.want || got.PicoUSD != 0 {
				t.Fatalf("estimate=%+v", got)
			}
		})
	}
}

func TestEstimatorDoesNotReuseRateForDifferentRuntimeModel(t *testing.T) {
	estimator := NewEstimator("https://api.deepseek.com", "deepseek-v4-pro")
	got := estimator.EstimateForModel(provider.Usage{InputTokens: 10, TotalTokens: 10}, true, "deepseek-v4-flash")
	if got.Status != StatusUnknown || got.UnknownReason != UnknownPriceUnavailable || got.Model != "deepseek-v4-flash" {
		t.Fatalf("estimate=%+v", got)
	}
}

func TestOriginDoesNotExposeCredentialsOrRequestPath(t *testing.T) {
	if got := Origin("https://user:password@API.Example:8443/v1?token=secret#fragment"); got != "https://api.example:8443" {
		t.Fatalf("origin=%q", got)
	}
}
