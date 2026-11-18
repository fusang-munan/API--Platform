export type Role = 'student' | 'mentor' | 'teacher'
export type UserStatus = 'pending_invite' | 'active' | 'disabled'
export type ResourceStatus = 'active' | 'inactive'
export type ModelStatus = 'pending_configuration' | 'active' | 'inactive'
export type ApiKeyStatus = 'pending_mentor' | 'pending_teacher' | 'approved' | 'active' | 'rejected' | 'inactive'
export type ApplicationStatus = 'pending' | 'approved' | 'rejected' | 'cancelled'
export type ModelCategory = 'text' | 'image' | 'audio' | 'video' | 'multimodal' | 'embedding' | 'rerank'
export type ModelCapability = 'reasoning'|'vision'|'tool_calling'|'structured_output'|'web_search'|'coding'|'embeddings'|'rerank'|'realtime'|'image_generation'|'video_generation'|'speech_to_text'|'text_to_speech'
export type BindingAdapter = 'openai_compatible' | 'openai_responses' | 'openai_embeddings' | 'openai_images' | 'openai_audio' | 'openai_video' | 'openai_realtime' | 'openai_moderations' | 'anthropic' | 'cohere_rerank_v2' | 'google_gemini_v1beta'
export type AuditAction = 'mentor_approved' | 'mentor_rejected' | 'teacher_approved' | 'teacher_rejected' | 'mentor_deactivated' | 'mentor_activated' | 'student_withdrawn'
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
export type Model = { model_id: string; display_name: string; description?: string | null; category: ModelCategory; capabilities: ModelCapability[]; input_modalities: string[]; output_modalities: string[]; context_window?: number | null; max_input_tokens?: number | null; max_output_tokens?: number | null; is_common: boolean; status: ModelStatus; route_ready: boolean; credit_multiplier: string | null; upstream_metadata?: Record<string, unknown>; provider_id?: string; provider_name?: string; primary_adapter?: BindingAdapter | null; anthropic_ready?: boolean; responses_ready?: boolean; credential_id?: string; credential_label?: string }
export type StudentModelPlaza = { common_models: Model[]; library_models: Model[] }
export type UsageSlice = { id: string; name: string; credits: string; calls?: number }
export type KeyUsage = { id: string; name: string; status: ApiKeyStatus; used: string; available?: string; pending: string; pending_review?: boolean; models: UsageSlice[] }
export type StudentProjectUsage = { project_id: string; project_name: string; quota: string; used: string; pending: string; available: string; pending_review?: boolean; keys: KeyUsage[] }
export type StudentUsage = { month: string; projects: StudentProjectUsage[] }
export type ProjectUsage = { month: string; project_id: string; project_name: string; quota: string; allocated: string; charged: string; pending: string; available: string; pending_review?: boolean; members: Array<{ user_id: string; user_name: string; quota: string; available: string; credits: string; pending_review?: boolean; keys: Array<{ id: string; name: string; status: ApiKeyStatus; used: string; pending_review?: boolean; models: UsageSlice[] }>; free_models: UsageSlice[] }>; models: UsageSlice[]; free_models: UsageSlice[] }
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
export type RequestedModel = { model_id: string; display_name?: string }
export type SubmitApiKey = { name: string; organization_id: string; project_id: string; models: RequestedModel[]; requested_monthly_credits: string }
export type ModelCatalogCandidate = { model_id:string;display_name?:string|null;description?:string|null;owned_by?:string|null;upstream_object?:string|null;upstream_created_at?:string|null;credential_id?:string;credential_label?:string;provider_name?:string;credit_multiplier?:string|null;raw_entry:Record<string, unknown> }
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
export type BeichenDataset = {
  credential_id: string
  kind: string
  last_success?: string | null
  last_error?: string | null
  effective_multiplier: number | null
}
type BeichenDisplayRates = Partial<Record<keyof BeichenRates, number | null>>
export type BeichenModel = {
  credential_id: string
  model: string
  mode: BeichenPriceUpdate['mode']
  upstream_rates?: BeichenDisplayRates | null
  school_rates?: BeichenDisplayRates | null
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
export type BeichenOverview = {
  datasets: BeichenDataset[] | null
  models: BeichenModel[] | null
  comparison: BeichenComparison[] | null
}
export type ProbeEndpoint = 'chat_completions'|'responses'|'messages'|'embeddings'|'rerank'
export type ProbeResponse = { results:Array<{endpoint:ProbeEndpoint|'models';path:string;http_status:number;duration_ms:number;truncated:boolean;error?:string;limits:{context_window?:number;max_input_tokens?:number;max_output_tokens?:number}}>;configuration:{description?:string;category?:ModelCategory;capabilities?:ModelCapability[];input_modalities?:string[];output_modalities?:string[];context_window?:number;max_input_tokens?:number;max_output_tokens?:number} }
