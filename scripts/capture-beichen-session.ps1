param(
    [Parameter(Mandatory=$true)][string]$Certificate,
    [string]$Go = ''
)
$ErrorActionPreference = 'Stop'
$repo = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..')).Path
$certPath = (Resolve-Path -LiteralPath $Certificate).Path
if (-not $Go) {
    foreach ($candidate in @('D:\go-sdk\go\bin\go.exe', 'D:\nebula-beichen-qa\go124\go\bin\go.exe')) {
        if (Test-Path -LiteralPath $candidate) {
            $Go = $candidate
            break
        }
    }
}
if (-not $Go -or -not (Test-Path -LiteralPath $Go)) {
    throw '找不到 Go 编译器；请用 -Go 指定 go.exe。'
}
Push-Location -LiteralPath $repo
try {
    & $Go run ./cmd/seal-beichen -session -certificate $certPath
    if ($LASTEXITCODE -ne 0) {
        throw '北辰登录会话捕获失败。'
    }
} finally {
    Pop-Location
}
