package usage

// The teacher report and the student's own view use the same evidence and
// arithmetic after selecting their respective call scopes.
const studentUsageObservationStatsSQL = `
   sum(parts.input_tokens) AS input,sum(parts.output_tokens) AS output,
   sum(parts.cache_read_tokens) AS cache_read,sum(parts.cache_write_tokens) AS cache_write,
   sum(token_evidence.known_total) AS tokens,sum(token_evidence.known_total-breakdown.classified) AS unclassified,
   count(*) AS coverage_calls,count(token_evidence.known_total) AS known_calls,
   count(*) FILTER (WHERE token_evidence.complete) AS complete_calls,
   count(*) FILTER (WHERE c.billing_state='charged') AS recorded_charged_calls,
   count(*) FILTER (WHERE c.beichen_state='pending_review') AS review_calls,
   sum(c.unresolved_credit_milli) FILTER (WHERE c.beichen_state='pending_review') AS review_credits,
   count(*) FILTER (WHERE token_evidence.cache_rate_input>0) AS cache_sample_calls,
   sum(parts.cache_read_tokens) FILTER (WHERE token_evidence.cache_rate_input>0) AS cache_sample_read,
   sum(token_evidence.cache_rate_input) FILTER (WHERE token_evidence.cache_rate_input>0) AS cache_sample_input`

const studentUsageObservationFilterSQL = `(
   c.billing_state='charged' OR c.beichen_state='pending_review'
   OR (c.outcome<>'pending' AND token_evidence.known_total IS NOT NULL)
 )`

const studentUsageLeafStatsSQL = `
   l.milli,l.calls,o.input,o.output,o.cache_read,o.cache_write,o.tokens,o.unclassified,o.known_calls,o.complete_calls,
   COALESCE(o.coverage_calls,0)+GREATEST(0,COALESCE(l.calls,0)-COALESCE(o.recorded_charged_calls,0)) AS coverage_calls,
   o.review_calls,o.review_credits,o.cache_sample_calls,o.cache_sample_read,o.cache_sample_input`

const studentUsageSumsSQL = `
   COALESCE(sum(milli),0) AS milli,COALESCE(sum(calls),0)::bigint AS calls,
   sum(input) AS input,sum(output) AS output,sum(cache_read) AS cache_read,sum(cache_write) AS cache_write,
   sum(tokens) AS tokens,sum(unclassified) AS unclassified,
   COALESCE(sum(coverage_calls),0)::bigint AS coverage_calls,COALESCE(sum(known_calls),0)::bigint AS known_calls,
   COALESCE(sum(complete_calls),0)::bigint AS complete_calls,
   COALESCE(sum(review_calls),0)::bigint AS review_calls,COALESCE(sum(review_credits),0) AS review_credits,
   COALESCE(sum(cache_sample_calls),0)::bigint AS cache_sample_calls,
   sum(cache_sample_read) AS cache_sample_read,sum(cache_sample_input) AS cache_sample_input`

const studentUsageTotalsSQL = `
   trim_scale(r.milli*0.001)::text,r.calls,
   r.input::text,r.output::text,r.cache_read::text,r.cache_write::text,r.tokens::text,
   r.coverage_calls,r.known_calls,r.complete_calls,r.unclassified::text,
   r.review_calls,trim_scale(r.review_credits)::text,
   (r.cache_sample_read/NULLIF(r.cache_sample_input,0))::double precision,
   r.cache_sample_calls,r.cache_sample_read::text,r.cache_sample_input::text`

func (v *StudentUsageTotals) scanFields() []any {
	tokens, coverage, cache := &v.Tokens, &v.TokenCoverage, &v.CacheHitCoverage
	return []any{
		&v.Credits, &v.Calls,
		&tokens.Input, &tokens.Output, &tokens.CacheRead, &tokens.CacheWrite, &tokens.Total,
		&coverage.Calls, &coverage.KnownCalls, &coverage.CompleteCalls, &coverage.UnclassifiedTokens,
		&v.PendingReviewCalls, &v.PendingReviewCredits, &v.CacheHitRate,
		&cache.SampleCalls, &cache.CacheReadTokens, &cache.InputWithCacheReadTokens,
	}
}

func (v *StudentUsageTotals) formatForDisplay() {
	v.Credits = displayCredits(v.Credits)
	v.StudentUsageMetrics = v.StudentUsageMetrics.forDisplay()
}
