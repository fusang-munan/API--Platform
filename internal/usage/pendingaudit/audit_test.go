package pendingaudit

import "testing"

func i64(v int64) *int64     { return &v }
func f64(v float64) *float64 { return &v }

func TestAnalyzeExactWhenAllFieldsKnown(t *testing.T) {
	t.Parallel()
	res := Analyze(Input{
		Protocol: "anthropic", PromptTokens: i64(100), CompletionTokens: i64(20), TotalTokens: i64(140),
		CacheReadTokens: i64(15), CacheWriteTokens: i64(5),
		InputRate: f64(0.000001), OutputRate: f64(0.000002), CacheReadRate: f64(0.0000005), CacheWriteRate: f64(0.000001),
	}, 0.001, 0.01)
	if res.Class != ClassExact || res.UnknownTokens != 0 {
		t.Fatalf("expected exact: %+v", res)
	}
}

func TestAnalyzeExactWhenRatesAreEqual(t *testing.T) {
	t.Parallel()
	res := Analyze(Input{
		Protocol: "anthropic", PromptTokens: i64(1000), CompletionTokens: i64(100), TotalTokens: i64(3000),
		InputRate: f64(0.000001), OutputRate: f64(0.000002), CacheReadRate: f64(0.000001), CacheWriteRate: f64(0.000001),
	}, 0.001, 0.01)
	if res.Class != ClassExact || res.UnknownTokens != 1900 {
		t.Fatalf("equal rates should be exact: %+v", res)
	}
}

func TestAnalyzeNarrowWhenUnknownIsSmall(t *testing.T) {
	t.Parallel()
	res := Analyze(Input{
		Protocol: "anthropic", PromptTokens: i64(1000), CompletionTokens: i64(10), TotalTokens: i64(1110),
		InputRate: f64(0.000001), OutputRate: f64(0.000002), CacheReadRate: f64(0.0000005), CacheWriteRate: f64(0.000001),
	}, 0.001, 0.01)
	if res.Class != ClassNarrow || res.UnknownTokens != 100 || res.MaxErrorYuan > 0.001 {
		t.Fatalf("expected narrow: %+v", res)
	}
}

func TestAnalyzeWideWhenUnknownIsLarge(t *testing.T) {
	t.Parallel()
	res := Analyze(Input{
		Protocol: "anthropic", PromptTokens: i64(1000), CompletionTokens: i64(100), TotalTokens: i64(3000),
		InputRate: f64(0.000001), OutputRate: f64(0.000002), CacheReadRate: f64(0.0000005), CacheWriteRate: f64(0.00001),
	}, 0.001, 0.01)
	if res.Class != ClassWide || res.UnknownTokens != 1900 {
		t.Fatalf("expected wide: %+v", res)
	}
}

func TestAnalyzeUnknownWithoutCompletion(t *testing.T) {
	t.Parallel()
	res := Analyze(Input{Protocol: "anthropic", PromptTokens: i64(10), TotalTokens: i64(20)}, 0.001, 0.01)
	if res.Class != ClassUnknown {
		t.Fatalf("missing completion should be unknown: %+v", res)
	}
}

func TestAnalyzeUnknownOnContradictoryTotal(t *testing.T) {
	t.Parallel()
	res := Analyze(Input{
		Protocol: "anthropic", PromptTokens: i64(10), CompletionTokens: i64(10), TotalTokens: i64(15),
		CacheReadTokens: i64(0), CacheWriteTokens: i64(0),
		InputRate: f64(0.000001), OutputRate: f64(0.000002), CacheReadRate: f64(0.000001), CacheWriteRate: f64(0.000001),
	}, 0.001, 0.01)
	if res.Class != ClassUnknown {
		t.Fatalf("contradictory total should be unknown: %+v", res)
	}
}

func TestAnalyzeOpenAIMissingCacheRead(t *testing.T) {
	t.Parallel()
	res := Analyze(Input{
		Protocol: "openai_compatible", PromptTokens: i64(1000), CompletionTokens: i64(10), TotalTokens: i64(1010),
		InputRate: f64(0.000001), OutputRate: f64(0.000002), CacheReadRate: f64(0.0000001), CacheWriteRate: f64(0.00001),
	}, 0.001, 0.01)
	if res.UnknownTokens != 1000 || res.Class != ClassWide {
		t.Fatalf("openai missing read should bound prompt tokens: %+v", res)
	}
}
