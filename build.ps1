[CmdletBinding()]
param([string]$OutputDirectory)

$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($OutputDirectory)) {
    $OutputDirectory = Join-Path $PSScriptRoot 'dist'
}
Push-Location -LiteralPath $PSScriptRoot
try {
    $target = (& go env GOOS GOARCH) -join '/'
    if ($LASTEXITCODE -ne 0 -or $target -ne 'windows/amd64') {
        throw '请使用 Windows x64 版 Go 构建。'
    }

    $sdkPath = Join-Path $PSScriptRoot 'URnetworkSdk.dll'
    $sdkHash = '30BFEFB30AF5DB1D0BC3C141FE41BA5136CD2489737921925282C09CFED7523C'
    if ((Get-FileHash -LiteralPath $sdkPath -Algorithm SHA256).Hash -ne $sdkHash) {
        throw 'SDK 文件与此版本固定的官方 DLL 不一致。'
    }

    $source = Get-Content -Raw -LiteralPath 'portable_windows.go'
    $version = [regex]::Match($source, 'const appVersion = "([0-9.]+)"').Groups[1].Value
    if (-not $version) { throw '无法读取应用版本。' }

    & windres --target=pe-x86-64 desktop.rc -O coff -o desktop_windows_amd64.syso
    if ($LASTEXITCODE -ne 0) { throw '无法生成 Windows 图标和清单资源。' }
    & go test -buildvcs=false -count=1 .
    if ($LASTEXITCODE -ne 0) { throw '自动测试失败。' }

    $directory = New-Item -Path $OutputDirectory -ItemType Directory -Force
    $exe = Join-Path $directory.FullName "URnetwork-Desktop-v$version-Windows-x64.exe"
    & go build -buildvcs=false -trimpath '-ldflags=-H windowsgui' -o $exe .
    if ($LASTEXITCODE -ne 0) { throw '无法构建桌面版。' }
    Get-Item -LiteralPath $exe
}
finally {
    Pop-Location
}
