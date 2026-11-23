package pendingaudit

import (
	"encoding/json"
	"math"
	"slices"
	"testing"
)

func i64(v int64) *int64     { return &v }
func f64(v float64) *float64 { return &v }

type amounts struct {
	min, max, recommended float64
}

func TestAnalyzeAmounts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, protocol      string
		total, read, write  *int64
		share, readRate     *float64
		threshold, relative float64
		class               string
		missing             []string
		unknown, recR, recW int64
		want                amounts
	}{
		{
			name: "Anthropic all known", protocol: "anthropic", total: i64(140), read: i64(15), write: i64(5),
			class: ClassExact, recR: 15, recW: 5, want: amounts{1.54, 1.54, 1.54},
		},
		{
			name: "Anthropic all parts known without total", protocol: "anthropic", read: i64(15), write: i64(5),
			class: ClassExact, recR: 15, recW: 5, want: amounts{1.54, 1.54, 1.54},
		},
		{
			name: "Anthropic missing only write", protocol: "anthropic", total: i64(140), read: i64(15),
			class: ClassExact, missing: []string{"cache_write_tokens"}, unknown: 5,
			recR: 15, recW: 5, want: amounts{1.54, 1.54, 1.54},
		},
		{
			name: "Anthropic missing only read", protocol: "anthropic", total: i64(140), write: i64(5),
			class: ClassExact, missing: []string{"cache_read_tokens"}, unknown: 15,
			recR: 15, recW: 5, want: amounts{1.54, 1.54, 1.54},
		},
		{
			name: "Anthropic both missing and equal rates", protocol: "anthropic", total: i64(140), readRate: f64(.016),
			class: ClassExact, missing: []string{"cache_read_tokens", "cache_write_tokens"}, unknown: 20,
			recR: 10, recW: 10, want: amounts{1.72, 1.72, 1.72},
		},
		{
			name: "Anthropic share zero and absolute threshold equality", protocol: "anthropic", total: i64(140),
			share: f64(0), threshold: .24, class: ClassNarrow,
			missing: []string{"cache_read_tokens", "cache_write_tokens"}, unknown: 20,
			recR: 0, recW: 20, want: amounts{1.48, 1.72, 1.72},
		},
		{
			name: "Anthropic share half and relative threshold", protocol: "anthropic", total: i64(140),
			share: f64(.5), threshold: .23, relative: .17, class: ClassNarrow,
			missing: []string{"cache_read_tokens", "cache_write_tokens"}, unknown: 20,
			recR: 10, recW: 10, want: amounts{1.48, 1.72, 1.6},
		},
		{
			name: "Anthropic share one and thresholds exceeded", protocol: "anthropic", total: i64(140),
			share: f64(1), threshold: .23, relative: .16, class: ClassWide,
			missing: []string{"cache_read_tokens", "cache_write_tokens"}, unknown: 20,
			recR: 20, recW: 0, want: amounts{1.48, 1.72, 1.48},
		},
		{
			name: "Anthropic default share rounds odd read up", protocol: "anthropic", total: i64(141),
			class: ClassWide, missing: []string{"cache_read_tokens", "cache_write_tokens"}, unknown: 21,
			recR: 11, recW: 10, want: amounts{1.484, 1.736, 1.604},
		},
		{
			name: "Chat all known", protocol: "openai_compatible", total: i64(120), read: i64(30), write: i64(10),
			class: ClassExact, recR: 30, recW: 10, want: amounts{1.28, 1.28, 1.28},
		},
		{
			name: "Chat missing read", protocol: "openai_compatible", total: i64(120), write: i64(10),
			class: ClassWide, missing: []string{"cache_read_tokens"}, unknown: 90,
			recR: 45, recW: 10, want: amounts{.92, 1.46, 1.19},
		},
		{
			name: "Responses known read missing write", protocol: "openai_responses", total: i64(120), read: i64(30),
			class: ClassWide, missing: []string{"cache_write_tokens"}, unknown: 70,
			recR: 30, recW: 35, want: amounts{1.22, 1.64, 1.43},
		},
		{
			name: "Gemini with both caches missing and no total", protocol: "google_gemini_v1beta",
			class: ClassWide, missing: []string{"cache_read_tokens", "cache_write_tokens"}, unknown: 100,
			recR: 50, recW: 25, want: amounts{.8, 2, 1.25},
		},
		{
			name: "Chat share zero", protocol: "openai_compatible", share: f64(0),
			class: ClassWide, missing: []string{"cache_read_tokens", "cache_write_tokens"}, unknown: 100,
			recR: 0, recW: 50, want: amounts{.8, 2, 1.7},
		},
		{
			name: "Chat share one", protocol: "openai_compatible", share: f64(1),
			class: ClassWide, missing: []string{"cache_read_tokens", "cache_write_tokens"}, unknown: 100,
			recR: 100, recW: 0, want: amounts{.8, 2, .8},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := Input{
				Protocol: tc.protocol, PromptTokens: i64(100), CompletionTokens: i64(20), TotalTokens: tc.total,
				CacheReadTokens: tc.read, CacheWriteTokens: tc.write,
				InputRate: f64(.01), OutputRate: f64(.02), CacheReadRate: f64(.004), CacheWriteRate: f64(.016),
				PriorReadShare: tc.share,
			}
			if tc.readRate != nil {
				in.CacheReadRate = tc.readRate
			}
			got := Analyze(in, tc.threshold, tc.relative)
			checkAmounts(t, got, tc.want)
			if got.Class != tc.class || got.CanAutoSettle != (tc.class != ClassWide) || got.UnknownTokens != tc.unknown ||
				got.RecommendedRead != tc.recR || got.RecommendedWrite != tc.recW || !slices.Equal(got.Missing, tc.missing) || got.Note == "" {
				t.Fatalf("unexpected audit result: %+v", got)
			}
			if math.Abs(got.RelativeError-(tc.want.max-tc.want.min)/tc.want.min) > 1e-12 {
				t.Errorf("relative error = %g, want interval width divided by %g", got.RelativeError, tc.want.min)
			}
		})
	}
}

func TestAnalyzeInvalidOrIncompleteEvidence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		change  func(*Input)
		missing []string
		unknown int64
	}{
		{"Anthropic total below known sum", func(in *Input) { in.TotalTokens = i64(139) }, nil, 0},
		{"Anthropic total above all known parts", func(in *Input) { in.TotalTokens = i64(141) }, nil, 0},
		{"Anthropic total and write missing", func(in *Input) { in.TotalTokens, in.CacheWriteTokens = nil, nil }, []string{"cache_write_tokens", "total_tokens"}, 0},
		{"Chat total incompatible", func(in *Input) { in.Protocol = "openai_compatible" }, nil, 0},
		{"Gemini cache exceeds prompt", func(in *Input) {
			in.Protocol, in.TotalTokens, in.CacheReadTokens = "google_gemini_v1beta", nil, i64(101)
		}, nil, 0},
		{"Responses combined cache exceeds prompt", func(in *Input) {
			in.Protocol, in.TotalTokens, in.CacheWriteTokens = "openai_responses", nil, i64(86)
		}, nil, 0},
		{"missing prompt", func(in *Input) { in.PromptTokens = nil }, []string{"prompt_tokens"}, 0},
		{"missing output", func(in *Input) { in.CompletionTokens = nil }, []string{"completion_tokens"}, 0},
		{"missing price for known usage", func(in *Input) { in.OutputRate = nil }, []string{"output_rate"}, 0},
		{"missing price for possible usage", func(in *Input) {
			in.Protocol, in.TotalTokens, in.CacheWriteTokens, in.CacheWriteRate = "openai_compatible", nil, nil, nil
		}, []string{"cache_write_tokens", "cache_write_rate"}, 85},
		{"negative count", func(in *Input) { in.CacheWriteTokens = i64(-1) }, nil, 0},
		{"invalid price", func(in *Input) { in.CacheReadRate = f64(math.NaN()) }, []string{"cache_read_rate"}, 0},
		{"invalid read share", func(in *Input) { in.PriorReadShare = f64(1.01) }, nil, 0},
		{"unknown protocol", func(in *Input) { in.Protocol = "unsupported" }, nil, 0},
		{"mixed modality Realtime", func(in *Input) { in.Protocol = "openai_realtime" }, nil, 0},
		{"overflowing token sum", func(in *Input) { in.PromptTokens = i64(math.MaxInt64) }, nil, 0},
		{"overflowing amount", func(in *Input) { in.InputRate = f64(math.MaxFloat64) }, nil, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := Input{
				Protocol: "anthropic", PromptTokens: i64(100), CompletionTokens: i64(20), TotalTokens: i64(140),
				CacheReadTokens: i64(15), CacheWriteTokens: i64(5),
				InputRate: f64(.01), OutputRate: f64(.02), CacheReadRate: f64(.004), CacheWriteRate: f64(.016),
			}
			tc.change(&in)
			got := Analyze(in, 100, 100)
			checkAmounts(t, got, amounts{})
			if got.Class != ClassUnknown || got.CanAutoSettle || got.UnknownTokens != tc.unknown ||
				got.RecommendedRead != 0 || got.RecommendedWrite != 0 || !slices.Equal(got.Missing, tc.missing) || got.Note == "" {
				t.Fatalf("unexpected audit result: %+v", got)
			}
		})
	}
}

func TestAnalyzeZeroAndLargeBalances(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                 string
		in                   Input
		threshold, relative  float64
		class                string
		relativeError        float64
		unknown, read, write int64
		want                 amounts
	}{
		{
			name: "zero remaining makes a missing price irrelevant", class: ClassExact,
			in: Input{Protocol: "openai_compatible", PromptTokens: i64(10), CompletionTokens: i64(0),
				CacheReadTokens: i64(10), CacheReadRate: f64(.01)},
			read: 10, want: amounts{.1, .1, .1},
		},
		{
			name: "zero lower bound cannot meet a finite relative threshold",
			in: Input{Protocol: "openai_compatible", PromptTokens: i64(10), CompletionTokens: i64(0),
				CacheWriteTokens: i64(0), InputRate: f64(.01), CacheReadRate: f64(0), PriorReadShare: f64(1)},
			relative: math.MaxFloat64, class: ClassWide, relativeError: math.MaxFloat64,
			unknown: 10, read: 10, want: amounts{0, .1, 0},
		},
		{
			name: "relative threshold equality",
			in: Input{Protocol: "anthropic", PromptTokens: i64(0), CompletionTokens: i64(0),
				TotalTokens: i64(10), CacheReadRate: f64(.02), CacheWriteRate: f64(.03)},
			relative: .5, class: ClassNarrow, relativeError: .5,
			unknown: 10, read: 5, write: 5, want: amounts{.2, .3, .25},
		},
		{
			name: "upper int64 balance still rounds to legal integer counts",
			in: Input{Protocol: "anthropic", PromptTokens: i64(0), CompletionTokens: i64(0), TotalTokens: i64(math.MaxInt64),
				CacheReadRate: f64(1), CacheWriteRate: f64(1)},
			class: ClassExact, unknown: math.MaxInt64, read: 1 << 62, write: 1<<62 - 1,
			want: amounts{float64(math.MaxInt64), float64(math.MaxInt64), float64(math.MaxInt64)},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Analyze(tc.in, tc.threshold, tc.relative)
			checkAmounts(t, got, tc.want)
			if got.Class != tc.class || got.CanAutoSettle != (tc.class != ClassWide) ||
				got.UnknownTokens != tc.unknown || got.RelativeError != tc.relativeError ||
				got.RecommendedRead != tc.read || got.RecommendedWrite != tc.write {
				t.Fatalf("unexpected audit result: %+v", got)
			}
		})
	}
}

func TestAnalyzeJSON(t *testing.T) {
	t.Parallel()
	in := Input{Protocol: "openai_compatible", PromptTokens: i64(1), CompletionTokens: i64(0),
		CacheReadTokens: i64(1), CacheReadRate: f64(2), PriorReadShare: f64(0)}
	inputJSON, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var inputFields map[string]any
	if err := json.Unmarshal(inputJSON, &inputFields); err != nil || inputFields["prior_read_share"] != float64(0) {
		t.Fatalf("unexpected input JSON: %s (%v)", inputJSON, err)
	}
	resultJSON, err := json.Marshal(Analyze(in, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	var resultFields map[string]any
	if err := json.Unmarshal(resultJSON, &resultFields); err != nil {
		t.Fatal(err)
	}
	for field, want := range map[string]any{
		"min_yuan": float64(2), "max_yuan": float64(2), "recommended_yuan": float64(2),
		"recommended_read": float64(1), "recommended_write": float64(0), "can_auto_settle": true,
	} {
		if got := resultFields[field]; got != want {
			t.Errorf("JSON %s = %v, want %v", field, got, want)
		}
	}
}

func TestAnalyzeBoundsAgainstAllSmallSplits(t *testing.T) {
	t.Parallel()
	const prompt int64 = 7
	const outputCost = 4
	for _, rates := range [][3]float64{
		{1, 2, 3}, {1, 3, 2}, {2, 1, 3}, {2, 3, 1}, {3, 1, 2}, {3, 2, 1},
	} {
		in := Input{
			Protocol: "openai_compatible", PromptTokens: i64(prompt), CompletionTokens: i64(2),
			InputRate: &rates[0], CacheReadRate: &rates[1], CacheWriteRate: &rates[2], OutputRate: f64(2),
		}
		minimum, maximum := math.Inf(1), math.Inf(-1)
		for read := int64(0); read <= prompt; read++ {
			for write := int64(0); write <= prompt-read; write++ {
				ordinary := prompt - read - write
				cost := float64(ordinary)*rates[0] + float64(read)*rates[1] + float64(write)*rates[2] + outputCost
				minimum = math.Min(minimum, cost)
				maximum = math.Max(maximum, cost)
			}
		}
		got := Analyze(in, 0, 0)
		// 7 * 0.5 rounds to 4 reads, leaving 1 write and 2 ordinary tokens.
		checkAmounts(t, got, amounts{minimum, maximum, 2*rates[0] + 4*rates[1] + rates[2] + outputCost})
		if got.RecommendedRead != 4 || got.RecommendedWrite != 1 {
			t.Errorf("rates %v produced an unexpected split: %+v", rates, got)
		}
	}
}

func checkAmounts(t *testing.T, got Result, want amounts) {
	t.Helper()
	if got.MinYuan > got.RecommendedYuan || got.RecommendedYuan > got.MaxYuan {
		t.Errorf("recommendation outside bounds: %+v", got)
	}
	for _, amount := range []struct {
		label     string
		got, want float64
	}{
		{"minimum", got.MinYuan, want.min},
		{"maximum", got.MaxYuan, want.max},
		{"recommendation", got.RecommendedYuan, want.recommended},
		{"estimated alias", got.EstimatedYuan, want.recommended},
		{"width", got.MaxErrorYuan, want.max - want.min},
	} {
		if math.IsNaN(amount.got) || math.Abs(amount.got-amount.want) > 1e-12 {
			t.Errorf("%s = %.15g, want %.15g", amount.label, amount.got, amount.want)
		}
	}
}
