export interface PendingAuditRow {
  id: string
  student: string
  model: string
  protocol: string
  reason: string
  class: 'exact' | 'narrow' | 'wide' | 'unknown'
  min: number
  max: number
  recommended: number
  note: string
  can_auto_settle: boolean
  student_credits?: string
}

export interface PendingAuditReport {
  provider_id: string
  generated_at: string
  total: number
  backlog_total: number
  next_offset: number | null
  exact: number
  narrow: number
  wide: number
  unknown: number
  auto_settle: number
  max_error_yuan: number
  estimated_auto_yuan: number
  preview_token: string
  rows: PendingAuditRow[]
  charged: number
  still_pending: number
  skipped: number
  errors: number
  credits_charged: string
  remaining_pending: number
}

export type Role = 'student' | 'mentor' | 'teacher'
export type UserStatus = 'pending_invite' | 'active' | 'disabled'
export type ResourceStatus = 'active' | 'inactive'
export type ModelStatus = 'pending_configuration' | 'active' | 'inactive'
export type ApiKeyStatus = 'pending_mentor' | 'pending_teacher' | 'approved' | 'active' | 'rejected' | 'inactive'
export type ApplicationStatus = 'pending' | 'approved' | 'rejected' | 'cancelled'
export type ModelCategory = 'text' | 'image' | 'audio' | 'video' | 'multimodal' | 'embedding' | 'rerank'
export type ModelCapability = 'reasoning'|'vision'|'tool_calling'|'structured_output'|'web_search'|'coding'|'embeddings'|'rerank'|'realtime'|'image_generation'|'video_generation'|'speech_to_text'|'text_to_speech'
export type BindingAdapter = 'openai_compatible' | 'openai_responses' | 'openai_embeddings' | 'openai_images' | 'openai_audio' | 'openai_video' | 'openai_realtime' | 'openai_moderations' | 'anthropic' | 'cohere_rerank_v2' | 'google_gemini_v1beta'
export type AuditAction = 'mentor_approved' | 'mentor_rejected' | 'teacher_approved' | 'teacher_rejected' | 'mentor_deactivated' | 'mentor_activated' | 'student_withdrawn' | 'student_model_added' | 'student_model_removed'
export type ProgressStep = 'submitted' | 'mentor_review' | 'teacher_review' | 'claimed'
export type ProgressCurrent = 'mentor_review' | 'teacher_review' | 'claim' | 'active' | 'rejected' | 'rejected_teacher' | 'inactive' | 'withdrawn'

export type User = { id: string; name: string; email: string; role: Role; status: UserStatus; created_at: string }
export type LoginChallenge = { user: User; identities: Role[] }
export type MeView = { user: User; identity: Role; identities: Role[] }
export type StudentAccount = { id: string; name: string; email: string; status: UserStatus; has_mentor_profile: boolean; mentor_profile_status?: ResourceStatus | null; mentor_granted_at?: string | null; created_at: string }
export type Organization = { id: string; name: string; description?: string | null; status: ResourceStatus }
export type MentorSummary = { id: string; name: string; email: string }
export type StudentProject = { id: string; organization_id: string; name: string; description?: string | null; status: ResourceStatus; has_mentor: boolean }
export type Project = { id: string; organization_id: string; name: string; description?: string | null; status: ResourceStatus; has_mentor: boolean; mentors?: MentorSummary[]; is_responsible?: boolean; application_status?: ApplicationStatus; monthly_credit_quota: string }
export type ModelProtocolInfo = {
  /** 网关实际发布的接入协议；与 /v1/models 的 protocols 一致。 */
  protocols?: string[]
  supported_protocols?: string[]
  available_protocols?: string[]
  recommended_protocol?: string | null
  probe_state?: 'passed' | 'unprobed' | 'failed' | 'not_applicable' | string
  last_probe_at?: string | null
  availability?: 'available' | 'degraded' | 'maintenance' | 'unverified' | 'unavailable' | 'not_published' | string
  availability_reason?: string
}
export type Model = { model_id: string; display_name: string; description?: string | null; category: ModelCategory; capabilities: ModelCapability[]; input_modalities: string[]; output_modalities: string[]; context_window?: number | null; max_input_tokens?: number | null; max_output_tokens?: number | null; is_common: boolean; status: ModelStatus; route_ready: boolean; credit_multiplier: string | null; upstream_metadata?: Record<string, unknown>; provider_id?: string; provider_name?: string; primary_adapter?: BindingAdapter | null; anthropic_ready?: boolean; responses_ready?: boolean; credential_id?: string; credential_label?: string } & ModelProtocolInfo
export type StudentModelPlaza = { common_models: Model[]; library_models: Model[] }
export type BeichenReceiptSpend = {
  beichen_base_credits?: string | null
  beichen_base_source?: string | null
  beichen_school_multiplier?: string | null
  beichen_charged_credits?: string | null
  beichen_actual_cost?: string | null
  beichen_unmatched_calls?: number | null
}
export type UsageSlice = BeichenReceiptSpend & { id: string; name: string; credits: string; calls?: number }
export type KeyUsage = { id: string; name: string; status: ApiKeyStatus; used: string; available?: string; pending: string; pending_review?: boolean; models: UsageSlice[] }
export type StudentProjectUsage = { project_id: string; project_name: string; quota: string; used: string; pending: string; available: string; pending_review?: boolean; keys: KeyUsage[] }
export type StudentUsage = { month: string; projects: StudentProjectUsage[] }
export type ProjectUsage = { month: string; project_id: string; project_name: string; quota: string; allocated: string; charged: string; pending: string; available: string; pending_review?: boolean; members: Array<BeichenReceiptSpend & { is_student?: boolean; user_id: string; user_name: string; quota: string; available: string; credits: string; pending?: string; pending_review?: boolean; keys: Array<{ id: string; name: string; status: ApiKeyStatus; used: string; pending_review?: boolean; models: UsageSlice[] }>; free_models: UsageSlice[] }>; models: UsageSlice[]; free_models: UsageSlice[] }
export type CallLog = { id: string; request_id: string; turn_id?: string; call_index?: number; project_id: string; project_name: string; user_id: string; user_name: string; api_key_id: string; api_key_name: string; model_id: string; model_name: string; provider_name: string; protocol: string; path: string; multiplier: string; credits: string; prompt_tokens?: number; completion_tokens?: number; total_tokens?: number; billing_state: string; outcome: string; error_category: string; error_message: string; created_at: string; attempts?: Array<{ provider_name: string; status: string; error_category: string; error_message: string; http_status?: number; latency_ms: number }> }
export type CallTurn = { id: string; project_id: string; project_name: string; user_id: string; user_name: string; api_key_id: string; api_key_name: string; model_id: string; model_name: string; task_key?: string; root_prompt: string; root_prompt_preview: string; root_prompt_bytes: number; call_count: number; credits: string; prompt_tokens: number; completion_tokens: number; total_tokens: number; outcome: string; started_at: string; finished_at?: string; calls?: CallLog[] }
export type InputMonitorItem = { call_id: string; project_id: string; project_name: string; user_id: string; user_name: string; api_key_name: string; model_name: string; provider_name: string; credits: string; outcome: string; preview: string; content_bytes: number; truncated: boolean; created_at: string; content?: string }
export type ProviderCredential = { id: string; provider_id: string; label: string; group_hint?: string | null; credential_configured: boolean; status: ResourceStatus; created_at: string; updated_at: string }
export type Provider = { id: string; name: string; base_url: string; credential_configured: boolean; status: ResourceStatus; credentials?: ProviderCredential[]; created_at: string; updated_at: string; last_catalog_sync?: ProviderCatalogSyncView }
export type Binding = { id: string; model_id: string; provider_id: string; credential_id?: string | null; credential_label?: string; upstream_model_name: string; adapter: BindingAdapter; priority: number; status: ResourceStatus; created_at: string; updated_at: string }
export type Progress = { current: ProgressCurrent; completed_steps: ProgressStep[] }
export type Audit = { action: AuditAction; actor_role: Role; comment?: string | null; created_at: string }
export type ApiKey = { id: string; name: string; status: ApiKeyStatus; route_ready: boolean; progress: Progress; applicant?: User; organization: Organization; project: Project; models: Model[]; audits?: Audit[]; key_prefix?: string | null; api_key?: string | null; claimed_at?: string | null; requested_monthly_credits?: string; mentor_monthly_credits?: string | null; monthly_credits?: string | null; member_monthly_credits?: string | null; member_used_credits?: string | null; member_pending_credits?: string | null; created_at: string; updated_at: string }
export type Application = { id: string; status: ApplicationStatus; mentor: User; project: Project; reviewer?: User; review_comment?: string | null; reviewed_at?: string | null; created_at: string }
export type MentorCandidate = { id: string; name: string; email: string; is_member: boolean }
export type Session = { access_token: string; token_type: string; expires_in: number; user: User; identity: Role }
export type PageMeta = { next_cursor?: string | null }
export type Envelope<T> = { data: T; meta?: PageMeta; request_id?: string }
export type ApiErrorDetails = { model_ids?: string[]; operation?: string; state_changed?: boolean } | Array<{ field: string; reason: string }>
export type ApiError = { error?: { code?: string; message?: string; details?: ApiErrorDetails }; request_id?: string }

export type OrganizationCreate = { name: string; description?: string; status?: ResourceStatus }
export type OrganizationUpdate = { name?: string; description?: string; status?: ResourceStatus }
export type ProjectBudget = { id: string; organization_id: string; name: string; status: ResourceStatus; month: string; quota: string; allocated: string; available: string; charged: string }
export type ProjectBudgetUpdate = { monthly_credit_quota: string }
export type ProjectCreate = { organization_id: string; name: string; description?: string; status?: ResourceStatus; monthly_credit_quota: string }
export type ProjectUpdate = { name?: string; description?: string; status?: ResourceStatus }
export type ProviderCreate = { name: string; base_url: string; credential: string; status?: ResourceStatus }
export type ProviderUpdate = { name?: string; base_url?: string; status?: ResourceStatus }
export type ProviderCredentialCreate = { label: string; group_hint?: string; credential: string; status?: ResourceStatus }
export type ProviderCredentialUpdate = { label?: string; group_hint?: string; credential?: string; status?: ResourceStatus }
export type ModelCreate = { model_id: string; display_name: string; description?: string; category: ModelCategory; capabilities: ModelCapability[]; input_modalities: string[]; output_modalities: string[]; context_window?: number; max_input_tokens?: number; max_output_tokens?: number; is_common?: boolean; status?: Exclude<ModelStatus, 'active'>; credit_multiplier?: string }
export type ModelUpdate = { model_id: string; display_name?: string; description?: string; category?: ModelCategory; capabilities?: ModelCapability[]; input_modalities?: string[]; output_modalities?: string[]; context_window?: number | null; max_input_tokens?: number | null; max_output_tokens?: number | null; is_common?: boolean; status?: ModelStatus; credit_multiplier?: string }
export type BindingRouteProbeEndpoint = 'chat_completions' | 'responses' | 'messages' | 'images_generations'
export type BindingRouteProbeStep = { endpoint: BindingRouteProbeEndpoint; path: string; adapter: BindingAdapter; http_status: number; ok: boolean; duration_ms: number; error?: string }
export type BindingCreate = { provider_id: string; upstream_model_name: string; adapter: BindingAdapter; priority: number; status: ResourceStatus; probed_adapters?: BindingAdapter[] }
export type BindingUpdate = { upstream_model_name?: string; adapter?: BindingAdapter; priority?: number; status?: ResourceStatus }
export type SubmitApiKey = { name: string; organization_id: string; project_id: string; requested_monthly_credits: string }
export type ModelCatalogCandidate = { model_id:string;display_name?:string|null;description?:string|null;owned_by?:string|null;upstream_object?:string|null;upstream_created_at?:string|null;credential_id?:string;credential_label?:string;provider_name?:string;credit_multiplier?:string|null;raw_entry:Record<string, unknown> } & ModelProtocolInfo
export type ModelCatalogPage = { items:ModelCatalogCandidate[];page:number;page_size:number;total:number }
export type ProviderCatalogSyncView = { provider_id: string; provider_name: string; synced: number; synced_at: string }
export type BeichenRates = { input: number; output: number; cache_read?: number; cache_write?: number }
export type BeichenPriceUpdate = { credential_id: string; model: string } & (
  { mode: 'follow' } | { mode: 'manual'; rates: BeichenRates }
)
export type BeichenImportKind = 'prices' | 'prices_base' | 'usage' | 'usage_export'
export type BeichenImport = { credential_id: string; csv: string } & (
  { kind: 'usage_export'; full_day_confirmed: true }
  | { kind: Exclude<BeichenImportKind, 'usage_export'>; full_day_confirmed?: never }
)
export type BeichenGroupStatus = 'pending' | 'assigned' | 'not_found' | 'not_allowed'
export type BeichenDataset = {
  credential_id: string
  kind: string
  last_success?: string | null
  last_error?: string | null
  effective_multiplier: number | string | null
  effective_multiplier_text?: string
  billing_state?: 'current' | 'stale' | 'unverified'
  group_id?: number
  group_name?: string
  group_rate?: string
  group_status?: BeichenGroupStatus
  official_price_missing?: boolean
  peak_enabled?: boolean
  peak_rate?: string
  observed_at?: string
  failure_count?: number
  alert?: boolean
}
type BeichenDisplayRates = Partial<Record<keyof BeichenRates, number | string | null>>
type BeichenPanelPrice = 'input_price' | 'output_price' | 'cache_read_price' | 'cache_write_price' |
  'cache_write_1h_price' | 'cache_write_12h_price' | 'image_input_price' | 'image_output_price' |
  'video_input_price' | 'video_output_price' | 'per_request_price'
export type BeichenPriceTier = Partial<Record<BeichenPanelPrice | 'min_tokens' | 'max_tokens', number | string | null>> & {
  billing_mode?: string | null
}
export type BeichenPriceConditions = BeichenPriceTier & {
  intervals?: BeichenPriceTier[] | null
  reasoning_effort_multipliers?: Record<string, number | string | null> | null
}
export type BeichenSchoolStatus = ModelStatus | 'not_configured'
export type BeichenModel = {
  credential_id: string
  model: string
  mode: BeichenPriceUpdate['mode']
  school_status?: BeichenSchoolStatus | null
  route_ready?: boolean | null
  price_status?: 'confirmed' | 'conditional' | 'derived' | 'not_listed' | 'pending' | 'pending_confirmation' | null
  price_source?: string
  price_observed_at?: string | null
  price_conditions?: BeichenPriceConditions | null
  price_conditions_exact?: BeichenPriceConditions | null
  upstream_rates?: BeichenDisplayRates | null
  school_rates?: BeichenDisplayRates | null
  price_covered?: boolean
}
export type BeichenComparison = {
  credential_id: string
  model: string
  upstream_usage_source: 'usage' | 'csv' | 'export' | null
  local_requests: number | null
  upstream_requests: number | null
  local_input_tokens: number | null
  upstream_input_tokens: number | null
  local_output_tokens: number | null
  upstream_output_tokens: number | null
  local_cache_read_tokens: number | null
  upstream_cache_read_tokens: number | null
  local_cache_write_tokens: number | null
  upstream_cache_write_tokens: number | null
  estimated_upstream_micros: number | null
  billed_upstream_micros: number | null
  trial_credit_milli: number | null
  trial_credits?: string | null
  charged_credits?: string | null
  pending_review_calls?: number | null
  trial_unpriced_calls: number | null
}
export type BeichenCrawlRun = {
  started_at: string
  duration_ms: number
  platform_count: number
  group_count: number
  model_count: number
  expected_model_count: number
  failures: Array<{ channel_id?: number; channel?: string; platform?: string; path: string; reason: string }>
  key_model_comparisons?: Array<{
    channel: string
    group_id: number | null
    state: 'checked' | 'unverified' | 'unavailable'
    error?: string
    matched: Array<{ key_model: string; channel_model: string; match: 'exact' | 'normalized'; price_complete: boolean }>
    only_key: string[]
    only_channel: string[]
  }>
  key_model_error?: string
  account_rate_error?: string
}
export type BeichenOverview = {
  web_login_required?: boolean | null
  website_sync?: BeichenWebsiteSync | null
  website_directory?: {
    state: 'fresh' | 'stale' | 'unverified'
    price_unit?: string
    last_success: string | null
    last_error: string | null
    last_run?: BeichenCrawlRun | null
  }
  maintenance_workspace?: { launch_url?: string | null } | null
  model_catalog?: BeichenCatalogModel[] | null
  credentials?: BeichenCredentialSummary[] | null
  key_capabilities?: BeichenKeyCapability[]
  catalog_differences?: BeichenCatalogDifference[] | null
  website_only_models?: BeichenWebsiteOnlyModel[]
  website_only_status?: 'compared' | 'stale' | 'unverified'
  probe_run?: BeichenProbeRun
  datasets: BeichenDataset[] | null
  models: BeichenModel[] | null
  comparison: BeichenComparison[] | null
}
export type BeichenHealthDimension = { state: 'available' | 'unavailable' | 'pending'; message: string }
export type BeichenCredentialSummary = {
  credential_id: string
  label: string
  status?: ResourceStatus
  health?: Record<'credential' | 'catalog' | 'pricing' | 'probe', BeichenHealthDimension> & { available_dimensions: number }
}
export type BeichenKeyUsageMetric = {
  date?: string
  model?: string
  requests: number | null
  input_tokens: number | null
  output_tokens: number | null
  cache_read_tokens: number | null
  cache_write_tokens: number | null
  total_tokens: number | null
  cost: string | null
  actual_cost: string | null
  account_cost: string | null
  observed_multiplier: string | null
  multiplier_diff: boolean
}
export type BeichenKeyCapability = {
  credential_id: string
  label: string
  website_key_id?: number | null
  website_group?: BeichenWebsiteGroup | null
  group_status?: BeichenGroupStatus
  models: string[]
  has_claude: boolean | null
  models_state: 'fresh' | 'stale' | 'failed' | 'pending'
  models_fetched_at: string | null
  models_error: string | null
  usage_state: BeichenKeyCapability['models_state']
  usage_fetched_at: string | null
  usage_error: string | null
  balance: string | null
  unit: string | null
  day_unit: string | null
  day_fetched_at: string | null
  daily_usage: BeichenKeyUsageMetric | null
  model_stats: BeichenKeyUsageMetric[]
  model_stats_day: string | null
  model_stats_fetched_at: string | null
  stored_multiplier: string | null
  target_multiplier: string | null
  expect_claude: boolean
  group_check: 'old_group' | 'mismatch' | 'target_observed' | 'pending'
}
export type BeichenCatalogDifference = {
  credential_id: string
  label: string
  state: 'compared' | 'stale' | 'unverified'
  group_id: number | null
  checked_at: string | null
  website_only: string[]
  key_only: string[]
}
export type BeichenWebsiteOnlyModel = {
  model: string
  channels: Array<{ credential_id: string; label: string }>
}
export type BeichenWebsiteGroup = {
  id?: number
  name?: string
  status?: string
  rate_multiplier?: number | string
  account_rate_multiplier?: number | string
  peak_rate_multiplier?: number | string
  peak_rate_enabled?: boolean
}
export type BeichenWebsiteLocation = {
  name: string
  channel_id?: number
  channel?: string
  platform?: string
  /** 官网价的作用域：platform 可跨组作模型级基础价；group（含旧数据缺省）只属于所列分组。 */
  price_scope?: 'platform' | 'group'
  groups: BeichenWebsiteGroup[]
  observed_at: string
  has_price: boolean
  price_complete?: boolean
  price_conditions_exact?: BeichenPriceConditions
  stale?: boolean
}
export type BeichenCatalogKey = {
  credential_id: string
  label: string
  school_channel?: string
  replacement?: boolean
  group_id: number | null
  group_stale?: boolean
  group_diagnosis?: 'model_outside_key_group' | 'price_outside_key_group' | 'group_rate_mismatch'
  state: 'listed' | 'missing' | 'pending' | 'failed' | 'ambiguous'
  upstream_model?: string
  match?: 'exact' | 'normalized' | 'alias'
  stale?: boolean
}
export type BeichenModelAlias = { credential_id: string | null; upstream_model: string; school_model: string }
export type BeichenModelAliasUpdate = Omit<BeichenModelAlias, 'school_model'> & { school_model: string | null }
export type BeichenRebindRoute = {
  credential_id: string | null
  credential_label: string | null
  upstream_model: string
  priority: number
  status: 'active' | 'inactive'
}
export type BeichenRebindPlan = {
  preview: boolean
  preview_token: string
  changes: Array<{
    model_id: string
    school_model: string
    adapter: string
    binding_id?: string
    action: 'add' | 'update' | 'enable' | 'disable'
    before: BeichenRebindRoute | null
    after: BeichenRebindRoute
  }>
}
export type BeichenProbeRun = {
  state: 'idle' | 'queued' | 'running' | 'completed' | 'failed'
  started_at: string | null
  finished_at: string | null
  error_code: string | null
}
export type BeichenAccountStatus = 'active' | 'expired' | 'error' | 'none'
export type BeichenWebsiteKey = {
  id: number
  name: string
  key_prefix: string | null
  group_id: number | null
  group_name: string | null
  rate: string | null
  status: 'active' | 'inactive' | 'disabled' | 'expired' | 'quota_exhausted' | 'unknown'
  enabled: boolean | null
  created_at: string | null
  last_seen: string | null
  credential_id: string | null
  match_status: 'matched' | 'unmatched' | 'ambiguous'
}
export type BeichenWebsiteKeyInventory = {
  status: 'unsynced' | 'current' | 'stale' | 'session_expired' | 'unavailable'
  last_seen: string | null
  keys: BeichenWebsiteKey[]
}
export type BeichenCrawlFailure = {
  channel_id?: number
  channel?: string
  platform?: string
  path?: string
  reason: string
}
export type BeichenCrawlSummary = {
  last_run_at: string | null
  platforms: number | string[] | null
  models: number | string[] | null
  failures: number | BeichenCrawlFailure[] | null
  failure_details?: BeichenCrawlFailure[]
}
export type BeichenAccount = {
  configured: boolean
  mode: 'password' | 'token'
  email: string
  status: BeichenAccountStatus
  expires_at: string | null
  last_verified_at: string | null
  last_error: string | null
  crawl: BeichenCrawlSummary
}
export type BeichenAccountLogin =
  | { mode: 'password'; email: string; password: string }
  | { mode: 'token'; token: string }
export type BeichenProbeResult = {
  provider_id: string
  credential_id: string
  model: string
  protocol: BindingAdapter
  status_code: number
  error_code: string | null
  duration_ms: number
  checked_at: string
  last_passed_at?: string
  detail: { max_output_tokens: number }
}
export type BeichenProbes = { run: BeichenProbeRun; results: BeichenProbeResult[] }
export type BeichenWebsiteSync = {
  channels: number
  priced_channels: number
  price_last_seen: string | null
  receipt_checked_channels: number
  receipt_last_checked: string | null
  receipt_caught_up_channels: number
}
export type BeichenReceiptItem = {
  occurred_at: string
  model: string
  student_name: string | null
  base_credits: string | null
  school_multiplier: string | null
  charged_credits: string | null
  actual_cost: string | null
  difference_credits: string | null
  matched: boolean
}
export type BeichenReceiptPage = { items: BeichenReceiptItem[]; next_cursor: string | null }
export type BeichenUsageRange = 'day' | 'month' | 'all'
export type BeichenUsageTotals = {
  credits: string
  tokens: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
  calls: number
  pending_calls: number
  students?: number
  keys?: number
  cache_hit_rate?: number | null
  tokens_missing?: number | null
  completeness?: 'complete' | 'partial' | 'missing' | null
  school_absorbed_credits?: string | null
}
export type BeichenModelUsageRow = BeichenUsageTotals & { model: string }
export type BeichenStudentModelUsage = { model: string; credits: string; tokens: number; calls: number }
export type BeichenStudentUsageRow = {
  user_id: string
  user_name: string
  project_id: string
  project_name: string
  credits: string
  tokens: number
  calls: number
  models: BeichenStudentModelUsage[]
}
export type BeichenKeyUsageRow = {
  api_key_id: string
  api_key_name: string
  user_id: string
  user_name: string
  project_name: string
  credits: string
  tokens: number
  calls: number
}
export type BeichenModelUsage = {
  range: BeichenUsageRange
  start: string | null
  end: string
  timezone: string
  totals: BeichenUsageTotals
  models: BeichenModelUsageRow[]
  students: BeichenStudentUsageRow[]
  keys: BeichenKeyUsageRow[]
}
export type BeichenReconcileSide = {
  credits?: string
  tokens: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
  calls?: number
  requests: number
  completeness: 'complete' | 'partial' | 'missing'
  billed_micros?: number | null
  estimated_micros?: number | null
  source?: string | null
  actual_cost?: string | null
  unit?: string | null
  mixed_units?: boolean
  amounts_by_unit?: Array<{ unit: string | null; actual_cost: string | null }> | null
  stale?: boolean
  sources?: string[]
}
export type BeichenReconcileLocal = BeichenReconcileSide & { tokens_missing: number; charged_tokens_missing_calls: number }
export type BeichenReconcileMoney = {
  student_yuan: string
  base_yuan: string
  actual_yuan: string | null
  margin_yuan: string | null
  margin_rate: string | null
  margin_available: boolean
}
export type BeichenReconcileCoverage = {
  comparable: boolean
  eligible_dates: string[]
  excluded_days: Array<{ date: string; reasons: string[] }>
  comparable_days: number
  verified_matched_complete_token_calls: number
  days_expected: number
  days_present: number
  missing_days: string[]
  key_usage_days?: number
  key_usage_model_days?: number
  stale_key_usage_days?: number
  pending_calls: number
  unpriced_calls: number
  multiplier_mixed: boolean
  verified_matched_calls?: number | null
  coverage_kind?: 'full' | 'partial' | 'none' | null
}
export type BeichenReconcileModel = {
  model: string
  credential_id: string
  label: string | null
  match_status: 'matched' | 'local_only' | 'upstream_only'
  local: BeichenReconcileLocal
  upstream: BeichenReconcileSide
  student_yuan: string
  base_yuan: string
  actual_yuan: string | null
  margin_yuan: string | null
  margin_available: boolean
  local_share: string
  upstream_share: string
  school_absorbed_credits?: string | null
  completeness?: 'complete' | 'partial' | 'missing' | null
}
export type BeichenReconcileKey = {
  credential_id: string
  label: string | null
  local: BeichenReconcileLocal
  upstream: BeichenReconcileSide
  student_yuan: string
  base_yuan: string
  actual_yuan: string | null
  margin_yuan: string | null
  margin_available: boolean
  school_absorbed_credits?: string | null
  completeness?: 'complete' | 'partial' | 'missing' | null
}
export type BeichenReconcileView = {
  coverage: BeichenReconcileCoverage
  totals: { local: BeichenReconcileLocal; upstream: BeichenReconcileSide; money: BeichenReconcileMoney }
  models: BeichenReconcileModel[]
  keys: BeichenReconcileKey[]
}
export type BeichenReconcile = BeichenReconcileView & {
  range: BeichenUsageRange
  start: string | null
  end: string
  timezone: string
  comparable_only: BeichenReconcileView | null
  unmatched_total: number
  unmatched_next_cursor: string | null
  unmatched_scope: 'observed' | 'comparable'
  unmatched_upstream: Array<{
    occurred_at: string; row_id: string; upstream_user_id: string
    credential_id: string; label: string; model: string
    input_tokens: number | null; output_tokens: number | null
    cache_read_tokens: number | null; cache_write_tokens: number | null
    request_id: string | null; receipt_id: string | null
    local_status: string | null; local_source: 'unique_claim' | 'receipt_without_unique_claim' | null
  }>
}
export type BeichenPriceScope = { scope?: 'all' | 'credentials'; credential_ids?: string[] }
export type BeichenModelPriceUpdate = { model: string } & BeichenPriceScope & (
  { mode: 'follow' } | { mode: 'manual'; rates: { input: string; output: string; cache_read: string; cache_write: string } }
)
export type BeichenBatchEnableResult = {
  enabled: Array<{ model: string; model_id: string }>
  skipped: Array<{ model: string; reason: string; message: string }>
}
export type BeichenModelPriceResult = {
  saved: boolean
  credentials: Array<{ credential_id: string; label: string }>
  applied_credential_ids?: string[]
}
export type BeichenCatalogModel = {
  model: string
  school_model?: string | null
  school_status?: BeichenSchoolStatus | null
  can_enable?: boolean | null
  credential_ids?: string[]
  preferred_routes?: BeichenPreferredRoute[] | null
  key_count?: number | null
  source?: 'website' | 'key' | 'both'
  catalog_state?: 'website_only' | 'key_listed' | 'catalog_missing'
  catalog_keys?: BeichenCatalogKey[]
  website_locations?: BeichenWebsiteLocation[]
  route_count?: number | null
  ready_count?: number | null
  price_state?: 'confirmed' | 'conditional' | 'derived' | 'manual' | 'missing' | 'mixed' | null
  price_covered?: boolean
  price_blocker?: { code: string; message: string } | null
  blockers?: Array<{ code: string; message: string }> | null
  activation?: 'active' | 'ready' | 'blocked' | null
  protocols?: BindingAdapter[]
  probe_state?: 'unprobed' | 'passed' | 'failed'
  last_probe_at?: string | null
  binding_issue?: { code: string; message: string }
}
export type BeichenPreferredRoute = {
  school_model: string; upstream_model?: string; adapter: string; provider_id: string; credential_id?: string | null; credential_label?: string | null
  priority: number; state: 'ready' | 'candidate'; effective_multiplier?: number | string | null
  effective_multiplier_text?: string | null
}
export type BeichenProtocolCapability = {
  protocol: string
  binding_ready: boolean
  probe_state: 'passed' | 'unprobed' | 'failed' | string
  probe?: { checked_at: string; last_passed_at?: string; status_code: number; error_code?: string } | null
}
export type BeichenModelCapability = {
  model: string
  credential_id: string | null
  credential_label: string
  supported_protocols: string[]
  available_protocols: string[]
  recommended_protocol: string | null
  protocol_details: BeichenProtocolCapability[]
  catalog_state: string
  availability: 'available' | 'degraded' | 'maintenance' | 'unverified' | 'unavailable' | 'not_published' | string
  availability_reason?: string
  price_state: 'confirmed' | 'conditional' | 'manual' | 'missing' | 'mixed' | string
  price_blocker: { code: string; message: string; action_hint?: string } | null
  multiplier_state: string
  probe_state: 'passed' | 'unprobed' | 'failed' | 'not_applicable' | string
  binding_state: string
  action_hint: string
}
export type BeichenCapabilities = {
  capabilities: BeichenModelCapability[]
  gaps: Array<{ model: string; credential_id: string | null; credential_label: string; category: string; code: string; message: string; action_hint: string }>
}
export type ProbeEndpoint = 'chat_completions'|'responses'|'messages'|'embeddings'|'rerank'
export type ProbeResponse = { results:Array<{endpoint:ProbeEndpoint|'models';path:string;http_status:number;duration_ms:number;truncated:boolean;error?:string;limits:{context_window?:number;max_input_tokens?:number;max_output_tokens?:number}}>;configuration:{description?:string;category?:ModelCategory;capabilities?:ModelCapability[];input_modalities?:string[];output_modalities?:string[];context_window?:number;max_input_tokens?:number;max_output_tokens?:number} }
