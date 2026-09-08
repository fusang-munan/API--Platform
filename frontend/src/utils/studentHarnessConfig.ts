import type { BindingAdapter, Model } from '../api/types'
import { bindingAdapterMeta } from './bindingAdapters'

export type HarnessTool = 'codex' | 'claude' | 'cc-switch'

export const CODEX_MODEL_CATALOG_FILENAME = 'nebula-model-catalog.json'
export const CODEX_MODEL_CATALOG_TOML_PATH = `~/.codex/${CODEX_MODEL_CATALOG_FILENAME}`

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

const CODEX_HINT = /codex|gpt-5/i
const CLAUDE_HINT = /claude|opus|sonnet|haiku/i

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


export function codexHarnessModels(models: Pick<Model, 'model_id' | 'display_name' | 'provider_name' | 'primary_adapter' | 'route_ready' | 'responses_ready'>[]) {
  return models.filter(model => model.responses_ready === true)
}

export function claudeHarnessModels(models: Pick<Model, 'model_id' | 'display_name' | 'provider_name' | 'primary_adapter' | 'route_ready' | 'anthropic_ready'>[]) {
  return models.filter(model => model.anthropic_ready === true)
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
  const preferred = kind === 'codex' ? pickCodexModel(options) : pickClaudeModel(options)
  if (preferred) return preferred
  const alternate = kind === 'claude'
    ? options.find(model => model.model_id !== pickCodexModel(options))?.model_id
    : null
  return alternate ?? options[0]?.model_id ?? ''
}

export function buildStudentHarnessConfig(
  apiKey: string,
  models: Pick<Model, 'model_id' | 'display_name' | 'responses_ready'>[],
  origin = gatewayOrigin(),
  options?: { codexModel?: string | null; claudeModel?: string | null },
): StudentHarnessConfig {
  const gateway_origin = origin.replace(/\/$/, '')
  const gateway_api_base = `${gateway_origin}/v1`
  const anthropic_base_url = gateway_origin
  const codex_model = options?.codexModel ?? pickCodexModel(models)
  const claude_model = options?.claudeModel ?? pickClaudeModel(models)
  const provider_name = 'nebula'
  const codex_auth_json = JSON.stringify({ OPENAI_API_KEY: apiKey }, null, 2)
  const codexCatalogModels = models.filter(model => model.responses_ready === true)
  const codex_model_catalog_json = buildCodexModelCatalogJsonForModels(
    codexCatalogModels.length > 0
      ? codexCatalogModels
      : [{ model_id: codex_model ?? 'your-model-id', display_name: models.find(model => model.model_id === codex_model)?.display_name ?? codex_model ?? 'your-model-id' }],
  )
  const codex_config_toml = buildCodexConfigToml({
    providerName: provider_name,
    model: codex_model ?? 'your-model-id',
    gatewayApiBase: gateway_api_base,
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
    supported_reasoning_levels: [
      { effort: 'low', description: 'Fast responses with lighter reasoning' },
      { effort: 'medium', description: 'Balances speed and reasoning depth for everyday tasks' },
      { effort: 'high', description: 'Greater reasoning depth for complex problems' },
    ],
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
    input_modalities: ['text'],
    experimental_supported_tools: [],
  }
}

export function buildCodexModelCatalogJsonForModels(models: Pick<Model, 'model_id' | 'display_name'>[]) {
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

function buildCodexConfigToml(input: { providerName: string; model: string; gatewayApiBase: string }) {
  return [
    `model_provider = "${input.providerName}"`,
    `model = "${input.model}"`,
    `model_catalog_json = "${CODEX_MODEL_CATALOG_TOML_PATH}"`,
    'model_reasoning_effort = "medium"',
    'disable_response_storage = true',
    'preferred_auth_method = "apikey"',
    '',
    `[model_providers.${input.providerName}]`,
    `name = "${input.providerName}"`,
    `base_url = "${input.gatewayApiBase}"`,
    'wire_api = "responses"',
    'requires_openai_auth = true',
  ].join('\n')
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

/** CC Switch compact catalog entries (see codexProviderPresets.ts / deeplink PR #4431). */
export function buildCCSwitchModelCatalogEntries(
  models: Pick<Model, 'model_id' | 'display_name'>[],
) {
  const seen = new Set<string>()
  return models.flatMap(model => {
    const modelId = model.model_id.trim()
    const dedupeKey = modelId.toLowerCase()
    if (!modelId || seen.has(dedupeKey)) return []
    seen.add(dedupeKey)
    const displayName = model.display_name?.trim()
    return [{
      model: modelId,
      ...(displayName && displayName !== modelId ? { displayName } : {}),
    }]
  })
}

/** Inline JSON payload for CC Switch deeplink `config` (Base64). */
export function buildCCSwitchCodexInlineConfig(
  apiKey: string,
  models: Pick<Model, 'model_id' | 'display_name'>[],
) {
  return {
    auth: { OPENAI_API_KEY: apiKey },
    apiFormat: 'openai_responses',
    modelCatalog: {
      models: buildCCSwitchModelCatalogEntries(models),
    },
  }
}

export function buildCCSwitchImportLink(params: Record<string, string | undefined | null>) {
  const search = new URLSearchParams()
  search.set('resource', 'provider')
  for (const [key, value] of Object.entries(params)) {
    if (value != null && value !== '') search.set(key, value)
  }
  return `ccswitch://v1/import?${search.toString()}`
}

export function buildCCSwitchCodexLink(
  config: StudentHarnessConfig,
  apiKey: string,
  catalogModels: Pick<Model, 'model_id' | 'display_name'>[] = [],
) {
  const models = catalogModels.length > 0
    ? catalogModels
    : config.codex_model
      ? [{ model_id: config.codex_model, display_name: config.codex_model }]
      : []
  const inlineConfig = buildCCSwitchCodexInlineConfig(apiKey, models)
  return buildCCSwitchImportLink({
    app: 'codex',
    name: 'Nebula',
    endpoint: config.gateway_api_base,
    apiKey,
    model: config.codex_model,
    homepage: config.gateway_origin,
    notes: '由 Nebula 平台一键导入（含 model catalog）',
    enabled: 'true',
    configFormat: 'json',
    config: encodeBase64UTF8(JSON.stringify(inlineConfig)),
  })
}

export function buildCCSwitchClaudeLink(config: StudentHarnessConfig, apiKey: string) {
  const model = config.claude_model
  return buildCCSwitchImportLink({
    app: 'claude',
    name: 'Nebula',
    endpoint: config.anthropic_base_url,
    apiKey,
    model,
    haikuModel: model,
    sonnetModel: model,
    opusModel: model,
    homepage: config.gateway_origin,
    notes: '由 Nebula 平台一键导入',
    enabled: 'true',
  })
}

export function buildCodexCatalogPatchScript(config: StudentHarnessConfig, shell: 'bash' | 'powershell') {
  if (shell === 'powershell') {
    const codexDir = '$env:USERPROFILE\\.codex'
    const catalogPath = `"${codexDir}\\${CODEX_MODEL_CATALOG_FILENAME}"`
    return [
      '$ErrorActionPreference = "Stop"',
      `New-Item -ItemType Directory -Force -Path "${codexDir}" | Out-Null`,
      `@'`,
      config.codex_model_catalog_json,
      `'@ | Set-Content -Path ${catalogPath} -Encoding utf8`,
      `$cfg = Join-Path ${codexDir} "config.toml"`,
      'if (-not (Test-Path $cfg)) { New-Item -ItemType File -Path $cfg | Out-Null }',
      '$lines = Get-Content $cfg -ErrorAction SilentlyContinue',
      `$target = 'model_catalog_json = ${catalogPath}'`,
      'if ($lines -match "^model_catalog_json\\s*=") {',
      '  $lines = $lines -replace "^model_catalog_json\\s*=.*", $target',
      '} else {',
      '  $lines += $target',
      '}',
      '$lines | Set-Content -Path $cfg -Encoding utf8',
      'Write-Host "已写入 model catalog 并更新 config.toml（保留 CC-switch 其他配置）"',
      'Write-Host "请完全退出 Codex 后执行: codex debug models | Select-String luna"',
    ].join('\n')
  }
  return [
    '#!/usr/bin/env bash',
    'set -euo pipefail',
    'CODEX_DIR="${HOME}/.codex"',
    `CATALOG_PATH="${'${CODEX_DIR}'}/${CODEX_MODEL_CATALOG_FILENAME}"`,
    'mkdir -p "$CODEX_DIR"',
    `cat > "$CATALOG_PATH" <<'EOF'`,
    config.codex_model_catalog_json,
    'EOF',
    'CONFIG_PATH="$CODEX_DIR/config.toml"',
    'touch "$CONFIG_PATH"',
    'if grep -q "^model_catalog_json[[:space:]]*=" "$CONFIG_PATH"; then',
    '  sed -i.bak "s|^model_catalog_json[[:space:]]*=.*|model_catalog_json = \\"$CATALOG_PATH\\"|" "$CONFIG_PATH"',
    '  rm -f "$CONFIG_PATH.bak"',
    'else',
    '  printf \'\\nmodel_catalog_json = "%s"\\n\' "$CATALOG_PATH" >> "$CONFIG_PATH"',
    'fi',
    'echo "已写入 $CATALOG_PATH 并更新 config.toml（保留 CC-switch 其他配置）"',
    'echo "请完全退出 Codex 后执行: codex debug models | grep luna"',
  ].join('\n')
}

export function buildCodexInstallScript(config: StudentHarnessConfig, shell: 'bash' | 'powershell') {
  if (shell === 'powershell') {
    const codexDir = '$env:USERPROFILE\\.codex'
    return [
      '$ErrorActionPreference = "Stop"',
      `New-Item -ItemType Directory -Force -Path "${codexDir}" | Out-Null`,
      `@'`,
      config.codex_auth_json,
      `'@ | Set-Content -Path "${codexDir}\\auth.json" -Encoding utf8`,
      `@'`,
      config.codex_config_toml,
      `'@ | Set-Content -Path "${codexDir}\\config.toml" -Encoding utf8`,
      `@'`,
      config.codex_model_catalog_json,
      `'@ | Set-Content -Path "${codexDir}\\${CODEX_MODEL_CATALOG_FILENAME}" -Encoding utf8`,
      'Write-Host "Codex 配置已写入 %USERPROFILE%\\.codex（含 model catalog）"',
      'Write-Host "请完全退出 Codex 后重新启动"',
    ].join('\n')
  }
  return [
    '#!/usr/bin/env bash',
    'set -euo pipefail',
    'CODEX_DIR="${HOME}/.codex"',
    'mkdir -p "$CODEX_DIR"',
    "cat > \"$CODEX_DIR/auth.json\" <<'EOF'",
    config.codex_auth_json,
    'EOF',
    "cat > \"$CODEX_DIR/config.toml\" <<'EOF'",
    config.codex_config_toml,
    'EOF',
    `cat > "$CODEX_DIR/${CODEX_MODEL_CATALOG_FILENAME}" <<'EOF'`,
    config.codex_model_catalog_json,
    'EOF',
    'echo "Codex 配置已写入 ${CODEX_DIR}（含 model catalog）"',
    'echo "请完全退出 Codex 后重新启动"',
  ].join('\n')
}

export function buildClaudeInstallScript(config: StudentHarnessConfig, apiKey: string, shell: 'bash' | 'powershell') {
  const env: Record<string, string> = {
    ANTHROPIC_AUTH_TOKEN: apiKey,
    ANTHROPIC_BASE_URL: config.anthropic_base_url,
  }
  if (config.claude_model) {
    env.ANTHROPIC_MODEL = config.claude_model
    env.ANTHROPIC_DEFAULT_OPUS_MODEL = config.claude_model
    env.ANTHROPIC_DEFAULT_SONNET_MODEL = config.claude_model
    env.ANTHROPIC_DEFAULT_HAIKU_MODEL = config.claude_model
  }
  const envJson = JSON.stringify(env)

  if (shell === 'powershell') {
    const envPayload = JSON.stringify({ env }, null, 2)
    return [
      '$ErrorActionPreference = "Stop"',
      `$payload = @'`,
      envPayload,
      `'@`,
      '$path = Join-Path $env:USERPROFILE ".claude.json"',
      'if (Test-Path $path) {',
      '  $existing = Get-Content $path -Raw | ConvertFrom-Json',
      '  $incoming = $payload | ConvertFrom-Json',
      '  if (-not $existing.env) { $existing | Add-Member -NotePropertyName env -NotePropertyValue @{} }',
      '  foreach ($key in $incoming.env.PSObject.Properties.Name) {',
      '    $existing.env | Add-Member -NotePropertyName $key -NotePropertyValue $incoming.env.$key -Force',
      '  }',
      '  $existing | ConvertTo-Json -Depth 8 | Set-Content -Path $path -Encoding utf8',
      '} else {',
      '  $payload | Set-Content -Path $path -Encoding utf8',
      '}',
      'Write-Host "Claude Code 配置已写入 $path"',
    ].join('\n')
  }

  return [
    '#!/usr/bin/env bash',
    'set -euo pipefail',
    'python3 <<\'PY\'',
    'import json, os',
    'path = os.path.expanduser("~/.claude.json")',
    `incoming_env = json.loads(${JSON.stringify(envJson)})`,
    'if os.path.exists(path):',
    '    with open(path, encoding="utf-8") as handle:',
    '        existing = json.load(handle)',
    'else:',
    '    existing = {}',
    'existing.setdefault("env", {}).update(incoming_env)',
    'os.makedirs(os.path.dirname(path) or ".", exist_ok=True)',
    'with open(path, "w", encoding="utf-8") as handle:',
    '    json.dump(existing, handle, indent=2, ensure_ascii=False)',
    '    handle.write("\\n")',
    'print(f"Claude Code 配置已写入 {path}")',
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
