package usage

// The per-call split matches attributedColumnsSQL and attributedFromSQL in
// the teacher reports. Keep the alias c on the underlying gateway call.
// Arithmetic uses numeric so combining bigint counters cannot overflow.
const studentTokenEvidenceSQL = `
 CROSS JOIN LATERAL (
   SELECT COALESCE(c.cache_hit_tokens, CASE
     WHEN COALESCE(c.usage_reason,'response_interrupted')='response_interrupted'
       AND c.protocol IN ('openai_compatible','openai_responses','openai_realtime')
       AND c.cache_miss_tokens::numeric+c.cache_write_tokens=c.prompt_tokens
     THEN 0::bigint END) AS reads,
   COALESCE(c.cache_write_tokens, CASE
     WHEN COALESCE(c.usage_reason,'response_interrupted')='response_interrupted'
       AND c.protocol IN ('openai_compatible','openai_responses','openai_realtime')
       AND c.cache_miss_tokens::numeric+c.cache_hit_tokens=c.prompt_tokens
     THEN 0::bigint END) AS writes
 ) cache
 CROSS JOIN LATERAL (
   SELECT CASE
     WHEN c.protocol='anthropic' THEN c.prompt_tokens::numeric
     WHEN c.protocol='google_gemini_v1beta' AND cache.reads<=c.prompt_tokens
       THEN c.prompt_tokens::numeric-cache.reads+COALESCE(c.tool_prompt_tokens,0)
     WHEN cache.reads::numeric+cache.writes<=c.prompt_tokens
       AND (c.cache_miss_tokens IS NULL OR c.cache_miss_tokens::numeric+cache.reads+cache.writes=c.prompt_tokens)
       THEN c.prompt_tokens::numeric-cache.reads-cache.writes
     WHEN (cache.reads IS NULL OR cache.writes IS NULL) AND (c.prompt_tokens IS NULL OR
       c.cache_miss_tokens::numeric+COALESCE(cache.reads,0)+COALESCE(cache.writes,0)<=c.prompt_tokens)
       THEN c.cache_miss_tokens::numeric
     END AS input_tokens,
     CASE WHEN c.protocol='google_gemini_v1beta' AND num_nonnulls(c.completion_tokens,c.reasoning_tokens)>0
       THEN COALESCE(c.completion_tokens,0)::numeric+COALESCE(c.reasoning_tokens,0)
       ELSE c.completion_tokens::numeric END AS output_tokens,
     cache.reads::numeric AS cache_read_tokens, cache.writes::numeric AS cache_write_tokens
 ) parts
 CROSS JOIN LATERAL (
   SELECT num_nonnulls(parts.input_tokens,parts.output_tokens,parts.cache_read_tokens,parts.cache_write_tokens) AS part_count,
     COALESCE(parts.input_tokens,0)+COALESCE(parts.output_tokens,0)+
       COALESCE(parts.cache_read_tokens,0)+COALESCE(parts.cache_write_tokens,0) AS classified
 ) breakdown
 CROSS JOIN LATERAL (
   SELECT CASE WHEN breakdown.part_count>0 OR c.total_tokens IS NOT NULL OR c.prompt_tokens IS NOT NULL
     THEN GREATEST(breakdown.classified,c.total_tokens,
       CASE WHEN c.protocol<>'anthropic' AND c.prompt_tokens IS NOT NULL
         THEN c.prompt_tokens::numeric+COALESCE(c.tool_prompt_tokens,0)+COALESCE(parts.output_tokens,0) END)
     END AS known_total,
     breakdown.part_count=4 AND c.usage_state IS NOT DISTINCT FROM 'reported'
       AND (c.total_tokens IS NULL OR c.total_tokens=breakdown.classified) AS complete,
     parts.input_tokens+parts.cache_read_tokens AS cache_rate_input
 ) token_evidence
`
