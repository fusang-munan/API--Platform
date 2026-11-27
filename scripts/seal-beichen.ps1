param(
    [Parameter(Mandatory=$true)][string]$Certificate,
    [string]$Go = 'go',
    [switch]$SkipGemini
)
$ErrorActionPreference = 'Stop'
$certPath = (Resolve-Path -LiteralPath $Certificate).Path
$repo = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..')).Path
Push-Location -LiteralPath $repo
try {
    & $Go run ./cmd/seal-beichen -certificate $certPath -check
    if ($LASTEXITCODE -ne 0) { throw '学校收件公钥尚未核实。' }
    function Read-PrivateField([string]$label) {
        $secret = Read-Host -Prompt $label -AsSecureString
        try {
            return [System.Net.NetworkCredential]::new('', $secret).Password
        } finally {
            $secret.Dispose()
        }
    }
    $names = if ($SkipGemini) { 'CC1','GM1','G3' } else { 'Gemini','CC1','GM1','G3' }
    $channels = foreach ($name in $names) {
        @{ channel = $name; api_key = (Read-PrivateField "$name 北辰渠道 Key") }
    }
    $config = @{
        channels = @($channels)
    }
    $OutputEncoding = [System.Text.UTF8Encoding]::new($false)
    ($config | ConvertTo-Json -Compress -Depth 4) | & $Go run ./cmd/seal-beichen -certificate $certPath
    if ($LASTEXITCODE -ne 0) { throw '生成学校密文失败。' }
} finally {
    Remove-Variable config,channels -ErrorAction SilentlyContinue
    Pop-Location
}
