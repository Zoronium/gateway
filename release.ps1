# Builds release binaries, checksums, a dependency list and a signature into dist\.
# Usage: .\release.ps1 [-Key C:\path\to\release_key]   (omit -Key to skip signing)
param([string]$Key = "")
$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot

$targets = "windows/amd64", "windows/arm64", "linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64"
$env:CGO_ENABLED = "0"   # pure Go: no C compiler, no system libraries
$env:GOFLAGS = "-mod=readonly"

go mod verify
if ($LASTEXITCODE) { throw "go mod verify failed" }
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
if ($LASTEXITCODE) { throw "govulncheck found problems" }
go test ./...
if ($LASTEXITCODE) { throw "tests failed" }

Remove-Item dist -Recurse -Force -ErrorAction SilentlyContinue
New-Item -ItemType Directory dist | Out-Null

foreach ($t in $targets) {
    $os, $arch = $t -split "/"
    $name = "gateway-$os-$arch" + $(if ($os -eq "windows") { ".exe" } else { "" })
    $env:GOOS = $os; $env:GOARCH = $arch
    # -trimpath and an empty buildid keep paths and timestamps out, so the same source gives the same bytes
    go build -trimpath -ldflags "-s -w -buildid=" -o "dist\$name" .
    if ($LASTEXITCODE) { throw "build failed for $t" }
}
Remove-Item Env:GOOS, Env:GOARCH

# Dependency list: every module, version and the hash go.sum pins.
$lines = go list -m -f "{{if not .Main}}{{.Path}} {{.Version}}{{end}}" all | Where-Object { $_ } | ForEach-Object {
    $p, $v = $_ -split " "
    $sum = (Select-String -Path go.sum -SimpleMatch "$p $v h1:").Line -replace '^\S+ \S+ ', ''
    "$p $v $sum"
}
$goVersion = (go version) -replace '^go version ', ''
@("# Built with: $goVersion", "# module version go.sum-hash") + $lines | Set-Content dist\DEPENDENCIES.txt -Encoding utf8

# Checksums, with LF line endings so the signature checks the same everywhere.
$sums = Get-ChildItem dist -File | Where-Object Name -ne "SHA256SUMS" | Sort-Object Name | ForEach-Object {
    "$((Get-FileHash $_.FullName -Algorithm SHA256).Hash.ToLower())  $($_.Name)"
}
[IO.File]::WriteAllText("$PWD\dist\SHA256SUMS", ($sums -join "`n") + "`n")

if ($Key) {
    # SSH signatures: ssh-keygen ships with Windows, macOS and Linux, so verifying needs nothing to install.
    ssh-keygen -Y sign -f $Key -n gateway-release dist\SHA256SUMS | Out-Null
    if ($LASTEXITCODE) { throw "signing failed" }
    Copy-Item release\allowed_signers dist\allowed_signers
}
Write-Host "`nDone. Files in dist\:"; Get-ChildItem dist | Select-Object Name, Length
