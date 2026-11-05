# Optional Authenticode signing. Set these env vars or pass parameters:
#   LANBUS_PFX=path\to\cert.pfx  LANBUS_PFX_PASSWORD=...
param(
  [Parameter(Mandatory=$false)][string]$Pfx = $env:LANBUS_PFX,
  [Parameter(Mandatory=$false)][string]$Password = $env:LANBUS_PFX_PASSWORD,
  [string]$Timestamp = "http://timestamp.digicert.com"
)
$ErrorActionPreference = "Stop"
if (-not $Pfx) { throw "No PFX configured. Set LANBUS_PFX or pass -Pfx." }
$files = Get-ChildItem -Path "$PSScriptRoot\..\..\dist\lanbus-*-windows-amd64\bin\*.exe"
foreach ($f in $files) {
  & signtool sign /fd SHA256 /f $Pfx /p $Password /tr $Timestamp /td SHA256 $f.FullName
  if ($LASTEXITCODE -ne 0) { throw "signtool failed: $($f.Name)" }
  & signtool verify /pa $f.FullName
}
Write-Host "Signed $($files.Count) binaries."
