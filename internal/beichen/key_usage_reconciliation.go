package beichen

// Key totals come from the selected daily summary, even when model_stats is incomplete.
const reconciliationKeyFactsSQL = `
, key_facts AS (
 SELECT credential_id,day,requests,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,
   total_tokens,source,unit,actual_cost,stale FROM model_usage_rows WHERE source <> 'key_usage'
 UNION ALL
 SELECT credential_id,day,requests,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,
   total_tokens,'key_usage',unit,actual_cost,stale FROM key_usage_days
)`

// Aggregate the selected source per Key and day at model, Key and provider levels.
const reconciliationSourcesSQL = `
, mapped_usage AS (
 SELECT a.*, COALESCE(alias.school_model,a.model) AS school_model
 FROM selected_models a ` + joinModelAliasSQL + `
), upstream_rows AS (
 SELECT credential_id,school_model AS model,0 AS level,requests,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,
   total_tokens,source,unit,actual_cost,stale FROM mapped_usage
 UNION ALL
 SELECT a.credential_id,NULL,l.level,a.requests,a.input_tokens,a.output_tokens,a.cache_read_tokens,a.cache_write_tokens,
   a.total_tokens,a.source,a.unit,a.actual_cost,a.stale
 FROM selected_key_facts a CROSS JOIN (VALUES (1),(3)) l(level)
), upstream_group_rows AS (
 SELECT CASE WHEN level <> 3 THEN credential_id END AS credential_id,model,level,
   num_nonnulls(input_tokens,output_tokens,cache_read_tokens,cache_write_tokens) AS token_parts,
   total_tokens > COALESCE(input_tokens,0)+COALESCE(output_tokens,0)+COALESCE(cache_read_tokens,0)+COALESCE(cache_write_tokens,0) AS unclassified_tokens,
   GREATEST(total_tokens,COALESCE(input_tokens,0)+COALESCE(output_tokens,0)+COALESCE(cache_read_tokens,0)+COALESCE(cache_write_tokens,0)) AS tokens,
   requests,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,source,unit,actual_cost,stale,
   CASE WHEN requests=0 AND actual_cost=0 AND unit='unknown' THEN NULL ELSE COALESCE(unit,'CNY') END AS amount_unit
 FROM upstream_rows
), upstream_summary AS (
 SELECT credential_id,model,level,count(*) AS row_count,
   CASE WHEN min(token_parts) = 4 AND NOT COALESCE(bool_or(unclassified_tokens),false) THEN 'complete'
        WHEN max(token_parts) > 0 OR max(tokens) > 0 THEN 'partial' ELSE 'missing' END AS token_completeness,
   sum(tokens) AS tokens,
   COALESCE(sum(requests), 0) AS requests,
   COALESCE(sum(input_tokens), 0) AS input_tokens, COALESCE(sum(output_tokens), 0) AS output_tokens,
   COALESCE(sum(cache_read_tokens), 0) AS cache_read_tokens, COALESCE(sum(cache_write_tokens), 0) AS cache_write_tokens,
   CASE WHEN count(actual_cost) = count(*) AND count(DISTINCT amount_unit) <= 1
     THEN sum(actual_cost) END AS actual_cost,
   CASE WHEN count(DISTINCT amount_unit) = 1 AND count(unit) FILTER (WHERE amount_unit IS NOT NULL) > 0
        THEN min(amount_unit) END AS unit,
   count(DISTINCT amount_unit) > 1 AS mixed_units,
   bool_or(stale) AS stale,
   string_agg(DISTINCT source, ',' ORDER BY source) AS source
 FROM upstream_group_rows GROUP BY credential_id,model,level
), upstream_amounts AS (
 SELECT credential_id,model,level,CASE WHEN count(unit)>0 THEN amount_unit END AS unit,
   CASE WHEN count(actual_cost)=count(*) THEN sum(actual_cost) END AS actual_cost
 FROM upstream_group_rows WHERE amount_unit IS NOT NULL GROUP BY credential_id,model,level,amount_unit
)`
