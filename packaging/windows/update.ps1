# Minimal signed-release updater. Prefer shipping this with your own release URL.
param(
  [Parameter(Mandatory=$true)][string]$Url,
  [Parameter(Mandatory=$true)][string]$Sha256,
  [string]$InstallDir = (Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path))
)
$ErrorActionPreference = "Stop"
$tmp = Join-Path $env:TEMP ("lanbus-update-" + [guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Force -Path $tmp | Out-Null
$zip = Join-Path $tmp "update.zip"
Invoke-WebRequest -Uri $Url -OutFile $zip
$actual = (Get-FileHash $zip -Algorithm SHA256).Hash.ToLower()
if ($actual -ne $Sha256.ToLower()) { throw "SHA256 mismatch: $actual" }
Expand-Archive $zip -DestinationPath $tmp -Force
$payload = Get-ChildItem $tmp -Directory | Select-Object -First 1
if (-not $payload) { throw "payload not found" }
Get-Process lanbusd,lanbus-gui -ErrorAction SilentlyContinue | Stop-Process -Force
Copy-Item (Join-Path $payload.FullName "bin\*") (Join-Path $InstallDir "bin") -Force
Write-Host "Updated. Restart start-hub.cmd and start-gui.cmd."
