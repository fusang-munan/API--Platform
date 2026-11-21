import { describe, expect, it } from 'vitest'
import type { ApiKey, BindingAdapter, Model } from '../api/types'
import {
  buildCCSwitchClaudeLink,
  buildCCSwitchCodexInlineConfig,
  buildCCSwitchCodexLink,
  buildCCSwitchProviderName,
  buildCodexSetupScript,
  decodeBase64UTF8,
  encodeBase64UTF8,
  buildCodexModelCatalogJson,
  buildMinimalCodexConfigToml,
  buildStudentHarnessConfig,
  CODEX_MODEL_CATALOG_FILENAME,
  orderCodexCatalogModels,
  claudeHarnessModels,
  claudeHarnessReady,
  codexHarnessModels,
  codexHarnessReady,
  codexSupportedReasoningEfforts,
  defaultHarnessModel,
  harnessDialogTitle,
  harnessModelOptions,
  harnessModelSummary,
  keyHarnessSupport,
  gatewayOriginIssue,
  keyCanWithdraw,
  keyIncludesModel,
  pickClaudeModel,
  pickCodexModel,
  selectCurrentStudentKey,
} from './studentHarnessConfig'
import { bindingAdapterMeta } from './bindingAdapters'

describe('studentHarnessConfig', () => {
  const models = [
    { model_id: 'gpt-5.3-codex', display_name: 'GPT Codex', provider_name: 'Relay A', primary_adapter: 'openai_responses' as BindingAdapter, route_ready: true, responses_ready: true },
    { model_id: 'claude-sonnet-4-5-20250929', display_name: 'Claude Sonnet', provider_name: 'Relay B', primary_adapter: 'anthropic' as BindingAdapter, route_ready: true, anthropic_ready: true },
  ]

  it('picks codex and claude models from whitelist', () => {
    expect(pickCodexModel(models)).toBe('gpt-5.3-codex')
    expect(pickClaudeModel(models)).toBe('claude-sonnet-4-5-20250929')
    expect(pickClaudeModel([{ model_id: 'gpt-5.6-luna' }])).toBeNull()
  })

  it('groups model options by client harness', () => {
    expect(harnessModelOptions(models)).toEqual(models)
    expect(codexHarnessModels(models).map(model => model.model_id)).toEqual(['gpt-5.3-codex'])
    expect(claudeHarnessModels(models).map(model => model.model_id)).toEqual(['claude-sonnet-4-5-20250929'])
    expect(defaultHarnessModel(models, 'codex')).toBe('gpt-5.3-codex')
    expect(defaultHarnessModel(models, 'claude')).toBe('claude-sonnet-4-5-20250929')
    expect(defaultHarnessModel([
      { model_id: 'gpt-5.6-luna', display_name: 'Luna', route_ready: true, responses_ready: true },
      { model_id: 'gpt-5.3-codex', display_name: 'Codex', route_ready: true, responses_ready: true },
    ], 'codex')).toBe('gpt-5.6-luna')
    expect(harnessModelSummary(models[0])).toContain('Relay A')
    expect(harnessModelSummary(models[0])).toContain(bindingAdapterMeta('openai_responses').label)
  })

  it('builds gateway-relative harness snippets with key-aligned defaults', () => {
    const config = buildStudentHarnessConfig(
      'neb_sk_test_key',
      [{ model_id: 'gpt-5.3-codex', display_name: 'GPT Codex', responses_ready: true }],
      'https://gateway.example',
      { codexModel: 'gpt-5.3-codex' },
    )
    expect(config.gateway_api_base).toBe('https://gateway.example/v1')
    expect(config.anthropic_base_url).toBe('https://gateway.example')
    expect(config.codex_auth_json).toContain('neb_sk_test_key')
    expect(config.codex_config_toml).toBe(buildMinimalCodexConfigToml('https://gateway.example/v1', {
      defaultModel: 'gpt-5.3-codex',
    }))
    expect(config.codex_config_toml).not.toContain('model_reasoning_effort')
    expect(config.codex_config_toml).not.toContain('preferred_auth_method')
    expect(config.codex_config_toml).not.toContain('name = ')
    expect(JSON.parse(config.codex_model_catalog_json).models[0].slug).toBe('gpt-5.3-codex')
    expect(JSON.parse(config.codex_model_catalog_json).models[0].supported_reasoning_levels).toHaveLength(3)
    expect(codexSupportedReasoningEfforts('gpt-5.6-luna')).toEqual(['low', 'medium', 'high', 'xhigh', 'max'])
    expect(codexSupportedReasoningEfforts('gpt-5.6-sol')).toEqual(['low', 'medium', 'high', 'xhigh', 'max', 'ultra'])
    expect(codexSupportedReasoningEfforts('gpt-5.3-codex')).toEqual(['low', 'medium', 'high'])
    expect(JSON.parse(config.codex_model_catalog_json).models[0].base_instructions).toBeTruthy()
    expect(JSON.parse(config.codex_model_catalog_json).models[0].default_reasoning_level).toBeUndefined()
    expect(JSON.parse(config.codex_model_catalog_json).models[0].context_window).toBe(272000)
    expect(config.claude_env_bash).toContain('ANTHROPIC_AUTH_TOKEN="neb_sk_test_key"')
  })

  it('keeps Claude base URLs free of /v1 and gives Codex exactly one /v1', () => {
    const config = buildStudentHarnessConfig(
      'neb_sk_test_key',
      [{ model_id: 'gpt-5.3-codex', display_name: 'Codex', responses_ready: true }],
      'https://gateway.example/v1/v1/',
    )
    expect(config.gateway_api_base).toBe('https://gateway.example/v1')
    expect(config.anthropic_base_url).toBe('https://gateway.example')
    expect(config.codex_config_toml).toContain('base_url = "https://gateway.example/v1"')
    expect(config.claude_env_bash).toContain('ANTHROPIC_BASE_URL="https://gateway.example"')
    expect(gatewayOriginIssue('https://gateway.example/v1')).toContain('不要包含 /v1')
    expect(gatewayOriginIssue('https://gateway.example')).toBe('')
  })

  it('only defaults to models that satisfy the selected protocol', () => {
    const rows = [
      { model_id: 'gpt-5.6-luna', display_name: 'Luna', route_ready: true, responses_ready: false },
      { model_id: 'gpt-5.3-codex', display_name: 'Codex', route_ready: true, responses_ready: true },
      { model_id: 'claude-sonnet-5', display_name: 'Sonnet 5', route_ready: true, anthropic_ready: false },
      { model_id: 'claude-sonnet-4-5', display_name: 'Sonnet 4.5', route_ready: true, anthropic_ready: true },
    ]
    expect(defaultHarnessModel(rows, 'codex')).toBe('gpt-5.3-codex')
    expect(defaultHarnessModel(rows, 'claude')).toBe('claude-sonnet-4-5')
  })

  it('selects the current key and prepares an original-quota reapply', () => {
    const model = (modelID: string) => ({
      model_id: modelID,
      display_name: modelID,
      category: 'text' as const,
      capabilities: [],
      input_modalities: ['text'],
      output_modalities: ['text'],
      is_common: true,
      status: 'active' as const,
      route_ready: true,
      credit_multiplier: '1.00',
    })
    const key = (overrides: Record<string, unknown>) => ({
      id: 'key',
      name: '我的 Key',
      status: 'active',
      route_ready: true,
      progress: { current: 'active', completed_steps: [] },
      organization: { id: 'org', name: 'Org', status: 'active' },
      project: { id: 'project', organization_id: 'org', name: 'Project', status: 'active', has_mentor: true, monthly_credit_quota: '500.000' },
      models: [model('gpt-5.3-codex')],
      requested_monthly_credits: '200.000',
      created_at: '2026-11-20T00:00:00Z',
      updated_at: '2026-11-20T00:00:00Z',
      ...overrides,
    }) as unknown as ApiKey

    const active = key({ id: 'active', status: 'active', monthly_credits: '300.000' })
    const pending = key({ id: 'pending', status: 'pending_mentor', requested_monthly_credits: '150.000', updated_at: '2026-11-21T00:00:00Z' })
    expect(selectCurrentStudentKey([pending, active])?.id).toBe('active')
    expect(keyIncludesModel(active, 'GPT-5.3-CODEX')).toBe(true)
    expect(keyIncludesModel(active, 'claude-sonnet-4-5')).toBe(false)
    expect(keyCanWithdraw(active)).toBe(false)
    expect(keyCanWithdraw(pending)).toBe(true)
    expect(selectCurrentStudentKey([key({ status: 'rejected' })])).toBeNull()
  })

  it('writes codex setup script with auth, catalog fallback, and minimal config', () => {
    const config = buildStudentHarnessConfig('neb_sk_test_key', [{ model_id: 'gpt-5.6-luna', display_name: 'Luna', responses_ready: true }], 'https://gateway.example')
    const script = buildCodexSetupScript(config, 'neb_sk_test_key')
    expect(script).toContain('auth.json')
    expect(script).toContain('catalogFallback')
    expect(script).toContain('configToml')
    expect(script).toContain(CODEX_MODEL_CATALOG_FILENAME)
    expect(script).toContain('/v1/codex/model_catalog')
    expect(script).toContain('model_catalog_json')
    expect(script).toContain('disable_response_storage = true')
    expect(script).toContain('网关 catalog 刷新跳过')
    expect(buildCodexModelCatalogJson('gpt-5.6-luna', 'Luna')).toContain('"display_name": "Luna"')
  })

  it('builds cc-switch codex deeplink with all key codex models in modelCatalog', () => {
    const multi = [
      { model_id: 'gpt-5.6-luna', display_name: 'Luna', route_ready: true, responses_ready: true },
      { model_id: 'gpt-5.6-terra', display_name: 'Terra', route_ready: true, responses_ready: true },
      { model_id: 'claude-sonnet-5', display_name: 'Claude', route_ready: true, anthropic_ready: true },
    ]
    const config = buildStudentHarnessConfig('neb_sk_test_key', multi, 'https://gateway.example', {
      codexModel: 'gpt-5.6-luna',
    })
    const codex = buildCCSwitchCodexLink(
      config,
      'neb_sk_test_key',
      codexHarnessModels(multi),
      '齐爱农',
      'gpt-5.6-luna',
      { includeInlineConfig: true },
    )
    const params = new URL(codex.replace('ccswitch://v1/import?', 'http://local/?')).searchParams
    const inline = JSON.parse(decodeBase64UTF8(params.get('config') ?? ''))
    expect(inline.modelCatalog.models.map((entry: { model: string }) => entry.model))
      .toEqual(['gpt-5.6-luna', 'gpt-5.6-terra'])
    expect(inline.apiFormat).toBe('openai_responses')
    expect(inline.config).toContain('model = "gpt-5.6-luna"')
    expect(inline.config).not.toContain('model_reasoning_effort')
    expect(inline.config).not.toContain('model_catalog_json')
    expect(params.get('model')).toBe('gpt-5.6-luna')
  })

  it('builds cc-switch codex deeplink matching cc-switch-codex-import.html base shape', () => {
    const config = buildStudentHarnessConfig('neb_sk_test_key', models, 'https://gateway.example', {
      codexModel: 'gpt-5.3-codex',
    })
    const codex = buildCCSwitchCodexLink(
      config,
      'neb_sk_test_key',
      codexHarnessModels(models),
      '齐爱农',
      'gpt-5.3-codex',
      { includeInlineConfig: true },
    )
    const claude = buildCCSwitchClaudeLink(config, 'neb_sk_test_key', '齐爱农')
    expect(codex).toMatch(/^ccswitch:\/\/v1\/import\?/)
    expect(codex).toContain('enabled=true')
    const params = new URL(codex.replace('ccswitch://v1/import?', 'http://local/?')).searchParams
    const inline = JSON.parse(decodeBase64UTF8(params.get('config') ?? ''))
    expect(inline.auth).toEqual({ OPENAI_API_KEY: 'neb_sk_test_key' })
    expect(inline.config).toContain('model_provider = "custom"')
    expect(inline.config).toContain('name = "custom"')
    expect(inline.modelCatalog.models.map((entry: { model: string }) => entry.model)).toEqual(['gpt-5.3-codex'])
    expect(claude).toContain('name=Nebula-%E9%BD%90%E7%88%B1%E5%86%9C')
  })

  it('lists only Responses-ready models for codex harness', () => {
    const multi = [
      { model_id: 'gpt-5.6-luna', display_name: 'Luna', route_ready: true, responses_ready: false },
      { model_id: 'gpt-5.3-codex', display_name: 'Codex', route_ready: true, responses_ready: true },
    ]
    expect(codexHarnessModels(multi).map(model => model.model_id)).toEqual(['gpt-5.3-codex'])
    expect(codexHarnessReady(multi[0])).toBe(false)
    const config = buildStudentHarnessConfig('neb_sk_test_key', multi, 'https://gateway.example')
    expect(JSON.parse(config.codex_model_catalog_json).models.map((model: { slug: string }) => model.slug))
      .toEqual(['gpt-5.3-codex'])
  })

  it('orders codex catalog with selected default model first', () => {
    const multi = [
      { model_id: 'gpt-5.6-luna', display_name: 'Luna' },
      { model_id: 'gpt-5.3-codex', display_name: 'Codex' },
    ]
    expect(orderCodexCatalogModels(multi, 'gpt-5.3-codex').map(model => model.model_id))
      .toEqual(['gpt-5.3-codex', 'gpt-5.6-luna'])
  })

  it('honours optional default model for claude only', () => {
    const config = buildStudentHarnessConfig('neb_sk_test_key', models, 'https://gateway.example', {
      codexModel: 'gpt-5.3-codex',
      claudeModel: 'claude-sonnet-4-5-20250929',
    })
    expect(config.codex_config_toml).toContain('model = "gpt-5.3-codex"')
    expect(config.codex_config_toml).not.toContain('model_reasoning_effort')
    expect(buildCCSwitchClaudeLink(config, 'neb_sk_test_key')).toContain('model=claude-sonnet-4-5-20250929')
  })

  it('excludes claude models from codex harness when dual routes exist', () => {
    const dual = [
      { model_id: 'claude-sonnet-5', display_name: 'Claude Sonnet 5', route_ready: true, responses_ready: true, anthropic_ready: false },
      { model_id: 'gpt-5.6-luna', display_name: 'Luna', route_ready: true, responses_ready: true },
    ]
    expect(codexHarnessModels(dual).map(model => model.model_id)).toEqual(['gpt-5.6-luna'])
    expect(claudeHarnessModels(dual)).toEqual([])
    expect(claudeHarnessReady(dual[0])).toBe(false)
    expect(keyHarnessSupport(dual)).toEqual({ codex: true, claude: false })
  })

  it('includes DeepSeek responses and anthropic routes in one-click setup', () => {
    const deepseek = [
      { model_id: 'deepseek-flash', display_name: 'deepseek-flash', route_ready: true, responses_ready: true, anthropic_ready: true },
    ]
    expect(codexHarnessModels(deepseek).map(model => model.model_id)).toEqual(['deepseek-flash'])
    expect(claudeHarnessModels(deepseek).map(model => model.model_id)).toEqual(['deepseek-flash'])
    expect(keyHarnessSupport(deepseek)).toEqual({ codex: true, claude: true })
  })

  it('limits harness imports to routable adapter kinds', () => {
    const gptOnly = [{ model_id: 'gpt-5.6-luna', display_name: 'Luna', route_ready: true, responses_ready: true }]
    const claudeOnly = [{ model_id: 'claude-sonnet-4-5-20250929', display_name: 'Sonnet', route_ready: true, anthropic_ready: true }]
    expect(keyHarnessSupport(gptOnly)).toEqual({ codex: true, claude: false })
    expect(keyHarnessSupport(claudeOnly)).toEqual({ codex: false, claude: true })
    expect(harnessDialogTitle({ codex: true, claude: false })).toBe('一键配置 Codex')
    expect(harnessDialogTitle({ codex: false, claude: true })).toBe('一键配置 Claude Code')
    expect(codexHarnessModels(gptOnly)).toHaveLength(1)
    expect(claudeHarnessModels(gptOnly)).toHaveLength(0)
  })
})
