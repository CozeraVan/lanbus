# Windows packaging

1. Build binaries:
   ```bat
   make go-hub go-worker go-gui
   ```
2. Build the portable directory + zip:
   ```bat
   python scripts\package_windows.py
   ```
   Output: `dist\lanbus-<version>-windows-amd64\` and `.zip`.

3. Optional Inno Setup installer:
   ```bat
   iscc /DMyAppVersion=1.9.0 packaging\windows\lanbus.iss
   ```
   Output: `dist\lanbus-<version>-setup.exe`.

4. Optional signing:
   ```powershell
   $env:LANBUS_PFX = "C:\path\cert.pfx"
   $env:LANBUS_PFX_PASSWORD = "..."
   powershell -ExecutionPolicy Bypass -File packaging\windows\sign.ps1
   ```

5. Optional update helper:
   ```powershell
   powershell -File packaging\windows\update.ps1 `
     -Url https://example/lanbus-2.0.0-windows-amd64.zip `
     -Sha256 <sha256>
   ```

The portable package ships the Go hub, Go worker, scanner, and Wails GUI.
Python is only needed for the optional `lanbus local` supervisor and the
Python hub fallback.
