$ErrorActionPreference = "Stop"

$installerPath = Join-Path $PSScriptRoot "install.ps1"
$tokens = $null
$parseErrors = $null
[System.Management.Automation.Language.Parser]::ParseFile($installerPath, [ref]$tokens, [ref]$parseErrors) | Out-Null
if ($parseErrors.Count -gt 0) {
    $parseErrors | ForEach-Object { Write-Error $_ }
    exit 1
}

if (Get-Module -ListAvailable PSScriptAnalyzer) {
    Invoke-ScriptAnalyzer -Path $installerPath -EnableExit
}

Write-Output "PowerShell installer validation passed"
