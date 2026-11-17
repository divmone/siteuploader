$ErrorActionPreference = 'Stop'
Push-Location -LiteralPath $PSScriptRoot
$resource = 'siteuploader_windows_amd64.syso'
$savedOS, $savedArch, $savedCgo = $env:GOOS, $env:GOARCH, $env:CGO_ENABLED
try {
    $env:GOOS = $null
    $env:GOARCH = $null
    $env:CGO_ENABLED = '0'
    go run github.com/akavel/rsrc@v0.10.2 -arch amd64 -manifest app.manifest -o $resource
    if ($LASTEXITCODE -ne 0) { throw 'Could not compile the Windows manifest.' }
    New-Item -ItemType Directory -Path dist -Force | Out-Null
    $targets = @(
        @{ OS = 'windows'; Arch = 'amd64'; File = 'SiteUploader.exe'; Flags = '-s -w -H=windowsgui' },
        @{ OS = 'linux'; Arch = 'amd64'; File = 'SiteUploader-linux-x64'; Flags = '-s -w' },
        @{ OS = 'linux'; Arch = 'arm64'; File = 'SiteUploader-linux-arm64'; Flags = '-s -w' },
        @{ OS = 'darwin'; Arch = 'amd64'; File = 'SiteUploader-macos-x64'; Flags = '-s -w' },
        @{ OS = 'darwin'; Arch = 'arm64'; File = 'SiteUploader-macos-arm64'; Flags = '-s -w' }
    )
    foreach ($target in $targets) {
        $env:GOOS = $target.OS
        $env:GOARCH = $target.Arch
        $file = Join-Path dist $target.File
        go build -trimpath -ldflags $target.Flags -o $file .
        if ($LASTEXITCODE -ne 0) { throw "Could not build $file." }
        Get-Item -LiteralPath $file | Select-Object FullName, Length
    }
} finally {
    $env:GOOS, $env:GOARCH, $env:CGO_ENABLED = $savedOS, $savedArch, $savedCgo
    if (Test-Path -LiteralPath $resource) { Remove-Item -LiteralPath $resource }
    Pop-Location
}
