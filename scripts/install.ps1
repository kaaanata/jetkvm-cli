[CmdletBinding()]
param(
    [string]$InstallDir = $(if ($env:JETKVM_INSTALL_DIR) { $env:JETKVM_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA "Programs\jetkvm" }),
    [switch]$Unmanaged
)

$ErrorActionPreference = "Stop"
$ReleaseTag = "__JETKVM_RELEASE_TAG__"
$Repository = "kaaanata/jetkvm-cli"
$ReleaseBaseUrl = if ($env:JETKVM_RELEASE_BASE_URL) { $env:JETKVM_RELEASE_BASE_URL } else { "https://github.com/$Repository/releases/download/$ReleaseTag" }
$releaseUri = [uri]$ReleaseBaseUrl
if ($releaseUri.Scheme -ne "https" -and $env:JETKVM_ALLOW_INSECURE_TEST_URL -ne "1") {
    throw "Release downloads require HTTPS."
}

$architecture = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture
$arch = switch ($architecture) {
    "X64" { "amd64" }
    "Arm64" { "arm64" }
    default { throw "Unsupported architecture: $architecture" }
}

$version = $ReleaseTag.Substring(1)
$archiveName = "jetkvm_${version}_windows_${arch}.zip"
$workDir = Join-Path ([System.IO.Path]::GetTempPath()) ("jetkvm-install-" + [guid]::NewGuid().ToString("N"))
[System.IO.Directory]::CreateDirectory($workDir) | Out-Null

try {
    $archivePath = Join-Path $workDir $archiveName
    $checksumPath = Join-Path $workDir "checksums.txt"
    $bundlePath = Join-Path $workDir "checksums.txt.sigstore.json"

    Invoke-WebRequest -UseBasicParsing -Uri "$ReleaseBaseUrl/$archiveName" -OutFile $archivePath
    Invoke-WebRequest -UseBasicParsing -Uri "$ReleaseBaseUrl/checksums.txt" -OutFile $checksumPath
    Invoke-WebRequest -UseBasicParsing -Uri "$ReleaseBaseUrl/checksums.txt.sigstore.json" -OutFile $bundlePath

    $archiveLength = (Get-Item -LiteralPath $archivePath).Length
    if ($archiveLength -le 0 -or $archiveLength -gt 128MB) { throw "Release archive size is outside the allowed range." }

    $cosign = Get-Command cosign -ErrorAction SilentlyContinue
    if ($cosign) {
        & $cosign.Source verify-blob $checksumPath `
            --bundle $bundlePath `
            --certificate-identity "https://github.com/$Repository/.github/workflows/release.yml@refs/heads/main" `
            --certificate-oidc-issuer "https://token.actions.githubusercontent.com" | Out-Null
        if ($LASTEXITCODE -ne 0) { throw "Sigstore verification failed." }
    }
    else {
        Write-Warning "cosign is not installed; continuing with mandatory SHA-256 verification"
    }

    $matches = @(Get-Content -LiteralPath $checksumPath | ForEach-Object {
        if ($_ -match '^([0-9a-fA-F]{64})\s+(.+)$' -and $Matches[2] -eq $archiveName) { $Matches[1].ToLowerInvariant() }
    })
    if ($matches.Count -ne 1) { throw "Release checksum entry is missing, invalid, or not unique for $archiveName." }
    $actual = (Get-FileHash -Algorithm SHA256 -LiteralPath $archivePath).Hash.ToLowerInvariant()
    if ($actual -ne $matches[0]) { throw "SHA-256 verification failed for $archiveName." }

    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $zip = [System.IO.Compression.ZipFile]::OpenRead($archivePath)
    try {
        $expected = @("jetkvm.exe", "LICENSE", "NOTICE", "README.md")
        if ($zip.Entries.Count -ne $expected.Count) { throw "Release archive contains an unexpected number of entries." }
        foreach ($entry in $zip.Entries) {
            if ($entry.FullName -notin $expected) { throw "Release archive contains an unexpected or unsafe path: $($entry.FullName)" }
            if ([string]::IsNullOrEmpty($entry.Name)) { throw "Release archive contains a directory entry." }
            if (($entry.ExternalAttributes -band 0xF0000000) -eq 0xA0000000) { throw "Release archive contains a symbolic link." }
        }
        foreach ($name in $expected) {
            if (@($zip.Entries | Where-Object FullName -eq $name).Count -ne 1) { throw "Release archive does not contain exactly one $name." }
        }

        $binaryEntry = $zip.GetEntry("jetkvm.exe")
        if ($binaryEntry.Length -le 0 -or $binaryEntry.Length -gt 64MB) { throw "Release executable size is outside the allowed range." }
        $stagedBinary = Join-Path $workDir "jetkvm.exe"
        [System.IO.Compression.ZipFileExtensions]::ExtractToFile($binaryEntry, $stagedBinary, $false)
    }
    finally {
        $zip.Dispose()
    }

    [System.IO.Directory]::CreateDirectory($InstallDir) | Out-Null
    $InstallDir = [System.IO.Path]::GetFullPath($InstallDir)
    $destination = Join-Path $InstallDir "jetkvm.exe"
    $installTemp = Join-Path $InstallDir (".jetkvm.install." + [guid]::NewGuid().ToString("N") + ".exe")
    Copy-Item -LiteralPath $stagedBinary -Destination $installTemp
    Move-Item -LiteralPath $installTemp -Destination $destination -Force

    $isUnmanaged = $Unmanaged -or $env:JETKVM_UNMANAGED_INSTALL -eq "1"
    if (-not $isUnmanaged) {
        $receiptPath = Join-Path $InstallDir ".jetkvm-install.json"
        $receipt = [ordered]@{
            schema_version = 1
            install_id = [guid]::NewGuid().ToString()
            owner = "standalone"
            executable = [System.IO.Path]::GetFullPath($destination)
            version = $version
            repository = $Repository
            channel = "stable"
            installed_at = [DateTime]::UtcNow.ToString("o")
        }
        $receiptTemp = Join-Path $InstallDir (".jetkvm-install." + [guid]::NewGuid().ToString("N"))
        $receipt | ConvertTo-Json -Compress | Set-Content -LiteralPath $receiptTemp -Encoding utf8NoBOM
        Move-Item -LiteralPath $receiptTemp -Destination $receiptPath -Force
    }
    else {
        Remove-Item -LiteralPath (Join-Path $InstallDir ".jetkvm-install.json") -Force -ErrorAction SilentlyContinue
    }

    if ($env:JETKVM_NO_MODIFY_PATH -ne "1") {
        $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
        $segments = @($userPath -split ';' | Where-Object { $_ })
        if ($InstallDir -notin $segments) {
            $nextPath = (@($segments) + $InstallDir) -join ';'
            [Environment]::SetEnvironmentVariable("Path", $nextPath, "User")
            Write-Output "Added $InstallDir to the user PATH; start a new terminal before running jetkvm."
        }
    }

    Write-Output "Installed jetkvm $version to $destination"
}
finally {
    Remove-Item -LiteralPath $workDir -Recurse -Force -ErrorAction SilentlyContinue
}
