import { describe, expect, it } from 'vitest'
import type { BindingAdapter, Model } from '../api/types'
import {
  buildCCSwitchClaudeLink,
  buildCCSwitchCodexLink,
  buildCCSwitchCodexInlineConfig,
  buildCodexCatalogPatchScript,
  buildCodexInstallScript,
  buildCodexModelCatalogJson,
  buildStudentHarnessConfig,
  CODEX_MODEL_CATALOG_FILENAME,
  CODEX_MODEL_CATALOG_TOML_PATH,
  decodeBase64UTF8,
  claudeHarnessModels,
  codexHarnessModels,
  defaultHarnessModel,
  harnessDialogTitle,
  harnessModelOptions,
  harnessModelSummary,
  keyHarnessSupport,
  pickClaudeModel,
  pickCodexModel,
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
    expect(harnessModelSummary(models[0])).toContain('Relay A')
    expect(harnessModelSummary(models[0])).toContain(bindingAdapterMeta('openai_responses').label)
  })

  it('builds gateway-relative harness snippets', () => {
    const config = buildStudentHarnessConfig('neb_sk_test_key', [{ model_id: 'gpt-5.3-codex', display_name: 'GPT Codex', responses_ready: true }], 'https://gateway.example')
    expect(config.gateway_api_base).toBe('https://gateway.example/v1')
    expect(config.anthropic_base_url).toBe('https://gateway.example')
    expect(config.codex_auth_json).toContain('neb_sk_test_key')
    expect(config.codex_config_toml).toContain('base_url = "https://gateway.example/v1"')
    expect(config.codex_config_toml).toContain('wire_api = "responses"')
    expect(config.codex_config_toml).toContain(`model_catalog_json = "${CODEX_MODEL_CATALOG_TOML_PATH}"`)
    expect(JSON.parse(config.codex_model_catalog_json).models[0].slug).toBe('gpt-5.3-codex')
    expect(JSON.parse(config.codex_model_catalog_json).models[0].shell_type).toBe('unified_exec')
    expect(JSON.parse(config.codex_model_catalog_json).models[0].context_window).toBe(272000)
    expect(config.claude_env_bash).toContain('ANTHROPIC_AUTH_TOKEN="neb_sk_test_key"')
  })

  it('writes model catalog in codex install script', () => {
    const config = buildStudentHarnessConfig('neb_sk_test_key', [{ model_id: 'gpt-5.6-luna', display_name: 'Luna', responses_ready: true }], 'https://gateway.example')
    const script = buildCodexCatalogPatchScript(config, 'bash')
    expect(script).toContain(CODEX_MODEL_CATALOG_FILENAME)
    expect(script).toContain('model_catalog_json')
    expect(script).toContain('"slug": "gpt-5.6-luna"')
    expect(buildCodexModelCatalogJson('gpt-5.6-luna', 'Luna')).toContain('"display_name": "Luna"')
  })

  it('builds CC-switch deep links for one-click import', () => {
    const config = buildStudentHarnessConfig('neb_sk_test_key', models, 'https://gateway.example')
    const codex = buildCCSwitchCodexLink(config, 'neb_sk_test_key', codexHarnessModels(models))
    const claude = buildCCSwitchClaudeLink(config, 'neb_sk_test_key')
    expect(codex).toMatch(/^ccswitch:\/\/v1\/import\?/)
    expect(codex).toContain('app=codex')
    expect(codex).toContain('apiKey=neb_sk_test_key')
    expect(codex).toContain('endpoint=https%3A%2F%2Fgateway.example%2Fv1')
    expect(codex).toContain('configFormat=json')
    const configParam = new URL(codex.replace('ccswitch://', 'https://ccswitch.local/')).searchParams.get('config')
    expect(configParam).toBeTruthy()
    const inline = JSON.parse(decodeBase64UTF8(configParam!))
    expect(inline.apiFormat).toBe('openai_responses')
    expect(inline.modelCatalog.models).toEqual([{ model: 'gpt-5.3-codex', displayName: 'GPT Codex' }])
    expect(buildCCSwitchCodexInlineConfig('neb_sk_test_key', [{ model_id: 'gpt-5.6-luna', display_name: 'Luna' }]))
      .toEqual({
        auth: { OPENAI_API_KEY: 'neb_sk_test_key' },
        apiFormat: 'openai_responses',
        modelCatalog: { models: [{ model: 'gpt-5.6-luna', displayName: 'Luna' }] },
      })
    expect(claude).toContain('app=claude')
    expect(claude).toContain('model=claude-sonnet-4-5-20250929')
  })

  it('honours selected models in generated config', () => {
    const config = buildStudentHarnessConfig('neb_sk_test_key', models, 'https://gateway.example', {
      codexModel: 'gpt-5.3-codex',
      claudeModel: 'claude-sonnet-4-5-20250929',
    })
    expect(config.codex_config_toml).toContain('model = "gpt-5.3-codex"')
    expect(buildCCSwitchClaudeLink(config, 'neb_sk_test_key')).toContain('model=claude-sonnet-4-5-20250929')
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
