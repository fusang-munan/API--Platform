import type { BindingAdapter, Model } from '../api/types'
import { bindingAdapterMeta } from './bindingAdapters'
import {
  codexReasoningLevelsForModel,
  codexSupportedReasoningEfforts,
} from './codexOfficialIndex'

export { codexSupportedReasoningEfforts } from './codexOfficialIndex'

export type HarnessTool = 'codex' | 'claude' | 'cc-switch'

export const CODEX_MODEL_CATALOG_FILENAME = 'nebula-model-catalog.json'
export const CODEX_MODEL_CATALOG_TOML_PATH = `~/.codex/${CODEX_MODEL_CATALOG_FILENAME}`

/** Required by Codex catalog loader — without this, model metadata (incl. reasoning picker) fails to load. */
export const CODEX_DEFAULT_BASE_INSTRUCTIONS =
  'You are Codex, a coding assistant connected through the Nebula gateway. Help the user with software engineering tasks.'

export const CODEX_REASONING_EFFORTS = ['low', 'medium', 'high'] as const
export type CodexReasoningEffort = typeof CODEX_REASONING_EFFORTS[number]

export type StudentHarnessConfig = {
  gateway_origin: string
  gateway_api_base: string
  anthropic_base_url: string
  codex_model: string | null
  claude_model: string | null
  provider_name: string
  codex_auth_json: string
  codex_config_toml: string
  codex_model_catalog_json: string
  claude_env_bash: string
  claude_env_powershell: string
  cc_switch_codex_auth_json: string
  cc_switch_codex_config_toml: string
  cc_switch_claude_json: string
}

const CODEX_HINT = /codex|gpt-/i
const CLAUDE_HINT = /claude|opus|sonnet|haiku/i

export function isClaudeModelId(modelId: string) {
  return CLAUDE_HINT.test(modelId.trim())
}

export function isCodexModelId(modelId: string) {
  return CODEX_HINT.test(modelId.trim())
}

const HARNESS_IMPORT_NOTES = '由 Nebula 平台一键导入'

export function buildCCSwitchProviderName(keyName?: string | null) {
  const trimmed = keyName?.trim()
  if (!trimmed) return 'Nebula'
  return `Nebula-${trimmed}`
}

export function gatewayOrigin(explicit?: string) {
  const trimmed = explicit?.trim()
  if (trimmed) return trimmed.replace(/\/$/, '')
  if (typeof window !== 'undefined' && window.location?.origin) return window.location.origin
  return 'http://localhost:5173'
}

export function pickCodexModel(models: Pick<Model, 'model_id'>[]) {
  return models.find(model => CODEX_HINT.test(model.model_id))?.model_id ?? null
}

export function pickClaudeModel(models: Pick<Model, 'model_id'>[]) {
  return models.find(model => CLAUDE_HINT.test(model.model_id))?.model_id ?? null
}


export function claudeHarnessReady(
  model: Pick<Model, 'model_id' | 'route_ready' | 'anthropic_ready'>,
) {
  return model.anthropic_ready === true
    || (isClaudeModelId(model.model_id) && model.route_ready === true)
}

export function codexHarnessModels(models: Pick<Model, 'model_id' | 'display_name' | 'provider_name' | 'primary_adapter' | 'route_ready' | 'responses_ready'>[]) {
  return models.filter(model => {
    if (isClaudeModelId(model.model_id)) return false
    if (isCodexModelId(model.model_id)) return true
    // DeepSeek and other non-GPT chat models still use the Codex Responses harness.
    return model.responses_ready === true
  })
}

export function codexHarnessReady(
  model: Pick<Model, 'model_id' | 'route_ready' | 'responses_ready'>,
) {
  return model.responses_ready === true || model.route_ready === true
}

export function claudeHarnessModels(models: Pick<Model, 'model_id' | 'display_name' | 'provider_name' | 'primary_adapter' | 'route_ready' | 'anthropic_ready'>[]) {
  return models.filter(model => claudeHarnessReady(model))
}

export type KeyHarnessSupport = {
  codex: boolean
  claude: boolean
}

export function keyHarnessSupport(
  models: Pick<Model, 'model_id' | 'display_name' | 'provider_name' | 'primary_adapter' | 'route_ready' | 'anthropic_ready' | 'responses_ready'>[],
): KeyHarnessSupport {
  return {
    codex: codexHarnessModels(models).length > 0,
    claude: claudeHarnessModels(models).length > 0,
  }
}

export function harnessDialogTitle(support: KeyHarnessSupport) {
  if (support.codex && support.claude) return '一键配置 Codex / Claude Code'
  if (support.codex) return '一键配置 Codex'
  if (support.claude) return '一键配置 Claude Code'
  return '客户端配置'
}

export function harnessModelOptions(models: Pick<Model, 'model_id' | 'display_name' | 'provider_name' | 'primary_adapter' | 'route_ready'>[]) {
  return models
}

export function harnessModelSummary(
  model: Pick<Model, 'model_id' | 'display_name' | 'provider_name' | 'primary_adapter' | 'route_ready' | 'anthropic_ready' | 'responses_ready'> | undefined,
  kind?: 'codex' | 'claude',
) {
  if (!model) return ''
  const parts = [model.display_name && model.display_name !== model.model_id ? model.display_name : model.model_id]
  if (model.provider_name) parts.push(`上游 ${model.provider_name}`)
  const adapter = kind === 'codex'
    ? (model.responses_ready ? 'openai_responses' : model.primary_adapter)
    : kind === 'claude'
      ? (model.anthropic_ready ? 'anthropic' : model.primary_adapter)
      : model.primary_adapter
  if (adapter) parts.push(bindingAdapterMeta(adapter as BindingAdapter).label)
  if (!model.route_ready) parts.push('路由未就绪')
  return parts.join(' · ')
}

export function defaultHarnessModel(
  models: Pick<Model, 'model_id' | 'display_name' | 'provider_name' | 'primary_adapter' | 'route_ready'>[],
  kind: 'codex' | 'claude',
) {
  const options = kind === 'codex' ? codexHarnessModels(models) : claudeHarnessModels(models)
  return options[0]?.model_id ?? ''
}

/** Put the selected default model first so catalog priority matches the Key default. */
export function orderCodexCatalogModels(
  models: Pick<Model, 'model_id' | 'display_name'>[],
  defaultModelId?: string | null,
) {
  const selected = defaultModelId?.trim()
  if (!selected) return models
  const match = models.find(model => model.model_id === selected)
  if (!match) return models
  return [match, ...models.filter(model => model.model_id !== selected)]
}

export function buildStudentHarnessConfig(
  apiKey: string,
  models: Pick<Model, 'model_id' | 'display_name' | 'responses_ready'>[],
  origin = gatewayOrigin(),
  options?: {
    codexModel?: string | null
    claudeModel?: string | null
    codexReasoningEffort?: CodexReasoningEffort | null
  },
): StudentHarnessConfig {
  const gateway_origin = origin.replace(/\/$/, '')
  const gateway_api_base = `${gateway_origin}/v1`
  const anthropic_base_url = gateway_origin
  const codex_model = options?.codexModel?.trim() || null
  const claude_model = options?.claudeModel?.trim() || null
  const codex_reasoning_effort = options?.codexReasoningEffort ?? null
  const provider_name = 'nebula'
  const codex_auth_json = JSON.stringify({ OPENAI_API_KEY: apiKey }, null, 2)
  const codexCatalogModels = orderCodexCatalogModels(
    codexHarnessModels(
      models as Pick<Model, 'model_id' | 'display_name' | 'responses_ready' | 'provider_name' | 'primary_adapter' | 'route_ready'>[],
    ),
    codex_model,
  )
  const codex_model_catalog_json = buildCodexModelCatalogJsonForModels(
    codexCatalogModels.length > 0
      ? codexCatalogModels
      : [{ model_id: 'your-model-id', display_name: 'your-model-id' }],
  )
  const codex_config_toml = buildMinimalCodexConfigToml(gateway_api_base, {
    defaultModel: codex_model,
    reasoningEffort: codex_reasoning_effort,
  })
  const claude_env_bash = buildClaudeEnvBash(apiKey, claude_model, anthropic_base_url)
  const claude_env_powershell = buildClaudeEnvPowerShell(apiKey, claude_model, anthropic_base_url)
  const cc_switch_claude_json = JSON.stringify({
    ANTHROPIC_AUTH_TOKEN: apiKey,
    ANTHROPIC_BASE_URL: anthropic_base_url,
    ...(claude_model ? {
      ANTHROPIC_MODEL: claude_model,
      ANTHROPIC_DEFAULT_OPUS_MODEL: claude_model,
      ANTHROPIC_DEFAULT_SONNET_MODEL: claude_model,
      ANTHROPIC_DEFAULT_HAIKU_MODEL: claude_model,
    } : {}),
  }, null, 2)

  return {
    gateway_origin,
    gateway_api_base,
    anthropic_base_url,
    codex_model,
    claude_model,
    provider_name,
    codex_auth_json,
    codex_config_toml,
    codex_model_catalog_json,
    claude_env_bash,
    claude_env_powershell,
    cc_switch_codex_auth_json: codex_auth_json,
    cc_switch_codex_config_toml: codex_config_toml,
    cc_switch_claude_json,
  }
}

export function buildCodexModelCatalogJson(model: string, displayName?: string | null) {
  return buildCodexModelCatalogJsonForModels([{ model_id: model, display_name: displayName ?? model }])
}

function codexCatalogEntry(
  model: Pick<Model, 'model_id' | 'display_name'>,
  priority: number,
) {
  const slug = model.model_id.trim()
  const name = model.display_name?.trim() && model.display_name.trim() !== slug ? model.display_name.trim() : slug
  return {
    slug,
    display_name: name,
    description: null,
    base_instructions: CODEX_DEFAULT_BASE_INSTRUCTIONS,
    supported_reasoning_levels: codexReasoningLevelsForModel(slug),
    shell_type: 'unified_exec',
    visibility: 'list',
    supported_in_api: true,
    priority,
    availability_nux: null,
    upgrade: null,
    support_verbosity: false,
    default_verbosity: null,
    apply_patch_tool_type: 'freeform',
    truncation_policy: { mode: 'tokens', limit: 10000 },
    context_window: 272000,
    max_context_window: 272000,
    effective_context_window_percent: 95,
    input_modalities: ['text'],
    supports_parallel_tool_calls: true,
    experimental_supported_tools: [],
  }
}

export function buildCodexModelCatalogJsonForModels(
  models: Pick<Model, 'model_id' | 'display_name'>[],
) {
  const seen = new Set<string>()
  const entries = models.flatMap((model, index) => {
    const slug = model.model_id.trim()
    const key = slug.toLowerCase()
    if (!slug || seen.has(key)) return []
    seen.add(key)
    return [codexCatalogEntry(model, index + 1)]
  })
  return JSON.stringify({ models: entries }, null, 2)
}

/** Codex config.toml — Key-aligned connectivity minimum. */
export function buildMinimalCodexConfigToml(
  gatewayApiBase: string,
  options?: {
    defaultModel?: string | null
    reasoningEffort?: CodexReasoningEffort | null
  },
) {
  const lines = ['model_provider = "custom"']
  const defaultModel = options?.defaultModel?.trim()
  if (defaultModel) lines.push(`model = "${defaultModel}"`)
  if (options?.reasoningEffort) lines.push(`model_reasoning_effort = "${options.reasoningEffort}"`)
  lines.push(
    `model_catalog_json = "${CODEX_MODEL_CATALOG_TOML_PATH}"`,
    'disable_response_storage = true',
    '',
    '[model_providers.custom]',
    `base_url = "${gatewayApiBase}"`,
    'wire_api = "responses"',
    'requires_openai_auth = true',
  )
  return lines.join('\n')
}

function buildClaudeEnvBash(apiKey: string, model: string | null, anthropicBaseUrl: string) {
  const lines = [
    `export ANTHROPIC_AUTH_TOKEN="${apiKey}"`,
    `export ANTHROPIC_BASE_URL="${anthropicBaseUrl}"`,
  ]
  if (model) lines.push(`export ANTHROPIC_MODEL="${model}"`)
  return lines.join('\n')
}

function buildClaudeEnvPowerShell(apiKey: string, model: string | null, anthropicBaseUrl: string) {
  const lines = [
    `$env:ANTHROPIC_AUTH_TOKEN = "${apiKey}"`,
    `$env:ANTHROPIC_BASE_URL = "${anthropicBaseUrl}"`,
  ]
  if (model) lines.push(`$env:ANTHROPIC_MODEL = "${model}"`)
  return lines.join('\n')
}

export function buildCCSwitchImportLink(params: Record<string, string | undefined | null>) {
  const search = new URLSearchParams()
  search.set('resource', 'provider')
  for (const [key, value] of Object.entries(params)) {
    if (value != null && value !== '') search.set(key, value)
  }
  return `ccswitch://v1/import?${search.toString()}`
}

export function encodeBase64UTF8(value: string) {
  if (typeof Buffer !== 'undefined') {
    return Buffer.from(value, 'utf8').toString('base64')
  }
  const bytes = new TextEncoder().encode(value)
  let binary = ''
  for (const byte of bytes) binary += String.fromCharCode(byte)
  return btoa(binary)
}

export function decodeBase64UTF8(value: string) {
  if (typeof Buffer !== 'undefined') {
    return Buffer.from(value, 'base64').toString('utf8')
  }
  const binary = atob(value)
  const bytes = Uint8Array.from(binary, char => char.charCodeAt(0))
  return new TextDecoder().decode(bytes)
}

/** CC-switch preset/deeplink modelCatalog shape (codexProviderPresets.ts + PR #4431). */
export function buildCCSwitchModelCatalogForCCSwitch(
  models: Pick<Model, 'model_id' | 'display_name'>[],
  options?: {
    defaultModel?: string | null
    reasoningEffort?: CodexReasoningEffort | null
  },
) {
  const defaultModel = options?.defaultModel?.trim()
  const reasoningEffort = options?.reasoningEffort ?? null
  const seen = new Set<string>()
  return {
    models: models.flatMap((model) => {
      const slug = model.model_id.trim()
      const key = slug.toLowerCase()
      if (!slug || seen.has(key)) return []
      seen.add(key)
      const displayName = model.display_name?.trim() && model.display_name.trim() !== slug
        ? model.display_name.trim()
        : slug
      return [{
        model: slug,
        displayName,
        contextWindow: 272000,
        reasoningLevels: codexSupportedReasoningEfforts(slug),
        ...(defaultModel === slug && reasoningEffort
          ? { defaultReasoningLevel: reasoningEffort }
          : {}),
      }]
    }),
  }
}

/** CC-switch deeplink inline config.toml — matches cc-switch-codex-import.html. */
export function buildCCSwitchCodexConfigToml(
  gatewayApiBase: string,
  options?: {
    defaultModel?: string | null
    reasoningEffort?: CodexReasoningEffort | null
  },
) {
  const provider = 'custom'
  const defaultModel = options?.defaultModel?.trim()
  const reasoningEffort = options?.reasoningEffort ?? null
  const lines = [`model_provider = "${provider}"`]
  if (defaultModel) lines.push(`model = "${defaultModel}"`)
  if (reasoningEffort) lines.push(`model_reasoning_effort = "${reasoningEffort}"`)
  lines.push(
    'disable_response_storage = true',
    '',
    `[model_providers.${provider}]`,
    `name = "${provider}"`,
    `base_url = "${gatewayApiBase}"`,
    'wire_api = "responses"',
    'requires_openai_auth = true',
  )
  return lines.join('\n')
}

/** CC-switch deeplink inline payload — auth + config.toml + Key-aligned modelCatalog. */
export function buildCCSwitchCodexInlineConfig(
  apiKey: string,
  options: {
    gatewayApiBase: string
    catalogModels: Pick<Model, 'model_id' | 'display_name'>[]
    defaultModel?: string | null
    reasoningEffort?: CodexReasoningEffort | null
  },
) {
  const payload: {
    auth: { OPENAI_API_KEY: string }
    config: string
    apiFormat?: string
    modelCatalog?: ReturnType<typeof buildCCSwitchModelCatalogForCCSwitch>
  } = {
    auth: { OPENAI_API_KEY: apiKey },
    config: buildCCSwitchCodexConfigToml(options.gatewayApiBase, {
      defaultModel: options.defaultModel,
      reasoningEffort: options.reasoningEffort,
    }),
  }
  const catalog = buildCCSwitchModelCatalogForCCSwitch(options.catalogModels, {
    defaultModel: options.defaultModel,
    reasoningEffort: options.reasoningEffort,
  })
  if (catalog.models.length > 0) {
    payload.apiFormat = 'openai_responses'
    payload.modelCatalog = catalog
  }
  return payload
}

export type CCSwitchCodexLinkOptions = {
  /** When true, CC-switch imports and switches immediately (same as Claude one-click). */
  enabled?: boolean
  includeInlineConfig?: boolean
}

/** CC-switch deeplink — inline auth + config.toml (official V1 provider import). */
export function buildCCSwitchCodexLink(
  config: StudentHarnessConfig,
  apiKey: string,
  catalogModels: Pick<Model, 'model_id' | 'display_name'>[] = [],
  keyName?: string | null,
  defaultModel?: string | null,
  options?: CCSwitchCodexLinkOptions & {
    reasoningEffort?: CodexReasoningEffort | null
  },
) {
  const models = orderCodexCatalogModels(
    catalogModels.length > 0
      ? catalogModels
      : [{ model_id: 'your-model-id', display_name: 'your-model-id' }],
    defaultModel,
  )
  const model = defaultModel?.trim() || models[0]?.model_id || undefined
  const params: Record<string, string | undefined | null> = {
    app: 'codex',
    name: buildCCSwitchProviderName(keyName),
    endpoint: config.gateway_api_base,
    apiKey,
    homepage: config.gateway_origin,
    notes: HARNESS_IMPORT_NOTES,
    enabled: options?.enabled === false ? 'false' : 'true',
    model,
  }
  if (options?.includeInlineConfig !== false) {
    const inlineConfig = buildCCSwitchCodexInlineConfig(apiKey, {
      gatewayApiBase: config.gateway_api_base,
      catalogModels: models,
      defaultModel: model,
      reasoningEffort: options?.reasoningEffort ?? null,
    })
    params.configFormat = 'json'
    params.config = encodeBase64UTF8(JSON.stringify(inlineConfig))
  }
  return buildCCSwitchImportLink(params)
}

export function buildCCSwitchClaudeLink(
  config: StudentHarnessConfig,
  apiKey: string,
  keyName?: string | null,
) {
  const model = config.claude_model
  return buildCCSwitchImportLink({
    app: 'claude',
    name: buildCCSwitchProviderName(keyName),
    endpoint: config.anthropic_base_url,
    apiKey,
    model: model ?? undefined,
    haikuModel: model ?? undefined,
    sonnetModel: model ?? undefined,
    opusModel: model ?? undefined,
    homepage: config.gateway_origin,
    notes: HARNESS_IMPORT_NOTES,
    enabled: 'true',
  })
}

/** macOS/Linux one-shot writer: 8-line config.toml + auth + catalog; optionally opens CC-switch (enabled=false). */
export function buildCodexSetupCommand(
  config: StudentHarnessConfig,
  apiKey: string,
  ccSwitchRegisterLink?: string | null,
) {
  const lines = [
    buildCodexSetupScript(config, apiKey),
  ]
  if (ccSwitchRegisterLink) {
    lines.push(
      '',
      'if [[ "$(uname -s)" == "Darwin" ]]; then',
      `  open ${JSON.stringify(ccSwitchRegisterLink)}`,
      'fi',
    )
  }
  lines.push(
    '',
    'echo ""',
    'echo "✓ 已写入 8 行 minimal config.toml（MCP 等段落已保留）"',
    'echo "✓ 已写入 auth.json 与 model catalog"',
    'echo "请完全退出 Codex 后重新启动"',
  )
  return lines.join('\n')
}

/** Writes auth.json, minimal config.toml, and Key catalog. */
export function buildCodexSetupScript(config: StudentHarnessConfig, apiKey: string) {
  const payload = JSON.stringify({
    gatewayOrigin: config.gateway_origin,
    gatewayApiBase: config.gateway_api_base,
    apiKey,
    catalogTomlPath: CODEX_MODEL_CATALOG_TOML_PATH,
    catalogFilename: CODEX_MODEL_CATALOG_FILENAME,
    catalogFallback: config.codex_model_catalog_json,
    configToml: config.codex_config_toml,
  })
  return [
    '#!/usr/bin/env bash',
    'set -euo pipefail',
    `python3 <<'PY'`,
    'import json, re, urllib.error, urllib.request',
    'from pathlib import Path',
    `payload = json.loads(${JSON.stringify(payload)})`,
    'gateway_origin = payload["gatewayOrigin"].rstrip("/")',
    'gateway_api_base = payload["gatewayApiBase"].rstrip("/")',
    'api_key = payload["apiKey"]',
    'catalog_toml_path = payload["catalogTomlPath"]',
    'codex_dir = Path.home() / ".codex"',
    'codex_dir.mkdir(parents=True, exist_ok=True)',
    'auth_path = codex_dir / "auth.json"',
    'catalog_file = codex_dir / payload["catalogFilename"]',
    'config_path = codex_dir / "config.toml"',
    'auth_path.write_text(json.dumps({"OPENAI_API_KEY": api_key}, indent=2) + "\\n", encoding="utf-8")',
    'root_drop = re.compile(r"^(model|model_reasoning_effort|model_provider|model_catalog_json|disable_response_storage)\\s*=")',
    'lines = config_path.read_text(encoding="utf-8").splitlines() if config_path.exists() else []',
    'rest = []',
    'in_custom_provider = False',
    'before_section = True',
    'for line in lines:',
    '    stripped = line.strip()',
    '    if stripped.startswith("[") and stripped.endswith("]"):',
    '        before_section = False',
    '        if stripped == "[model_providers.custom]":',
    '            in_custom_provider = True',
    '            continue',
    '        in_custom_provider = False',
    '        rest.append(line)',
    '        continue',
    '    if in_custom_provider:',
    '        continue',
    '    if before_section:',
    '        if not stripped or root_drop.match(stripped):',
    '            continue',
    '    rest.append(line)',
    'prefix = payload["configToml"].splitlines()',
    'output = prefix + ([""] if rest else []) + rest',
    'config_path.write_text("\\n".join(output).rstrip() + "\\n", encoding="utf-8")',
    'catalog_file.write_text(payload["catalogFallback"].rstrip() + "\\n", encoding="utf-8")',
    'try:',
    '    req = urllib.request.Request(',
    '        f"{gateway_origin}/v1/codex/model_catalog",',
    '        headers={"Authorization": f"Bearer {api_key}"},',
    '    )',
    '    with urllib.request.urlopen(req, timeout=60) as resp:',
    '        catalog_file.write_bytes(resp.read())',
    '    print("已从网关刷新 model catalog")',
    'except (urllib.error.URLError, urllib.error.HTTPError, TimeoutError, OSError) as err:',
    '    print(f"网关 catalog 刷新跳过（已写入 Key 模型列表）: {err}")',
    'print(f"已写入 {auth_path}")',
    'print(f"已写入 {catalog_file}")',
    'print(f"已写入 {config_path}")',
    'print("已写入 Key 对齐的 minimal config.toml（MCP 等段落已保留）")',
    'print("请完全退出 Codex 后重新启动；勿以 CC-switch 内 config 预览为准")',
    'PY',
  ].join('\n')
}

export function downloadTextFile(filename: string, content: string, mime = 'text/plain;charset=utf-8') {
  const blob = new Blob([content], { type: mime })
  const url = URL.createObjectURL(blob)
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = filename
  anchor.click()
  URL.revokeObjectURL(url)
}
