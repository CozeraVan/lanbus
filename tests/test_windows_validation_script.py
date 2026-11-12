from pathlib import Path
import re

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts" / "validate-windows.ps1"


def script_text() -> str:
    return SCRIPT.read_text(encoding="utf-8")


def test_script_has_supported_parameters():
    text = script_text()
    for name in ("Hub", "SkipBuild", "IncludeRecovery", "ReportDirectory", "KeepReports"):
        assert re.search(rf"\${name}\b", text)
    assert 'ValidateSet("both", "go", "python")' in text


def test_script_refuses_non_windows_and_never_changes_firewall():
    text = script_text()
    assert "Win32NT" in text
    assert "Windows 实机" in text
    for forbidden in ("New-NetFirewallRule", "Set-NetFirewallRule", "Register-ScheduledTask"):
        assert forbidden not in text


def test_script_isolated_and_cleans_recorded_process_trees():
    text = script_text()
    for required in ("Get-Random", "TcpListener", "finally", "taskkill", "/T", "/F", "Remove-Item"):
        assert required in text
    assert "Stop-Process -Name" not in text


def test_script_reports_failures_and_redacts_tokens():
    text = script_text()
    assert "summary.json" in text
    assert "summary.md" in text
    assert "exit 1" in text
    assert "ConvertTo-Json" in text
    assert "Write-Host $env:LANBUS_TOKEN" not in text
    assert "Write-Host $AgentToken" not in text
    assert "Write-Host $WorkerToken" not in text


def test_script_covers_state_security_utf8_and_optional_recovery():
    text = script_text()
    for required in ("max_retry", "max_recoveries", "rework", "evil.example.com",
                     "界面🙂", "cancel", "claim_token", "IncludeRecovery"):
        assert required in text


def test_script_refetches_full_task_after_rework():
    text = script_text()
    assert '$first = Invoke-McpTool $Base $AgentToken "lan_result"' in text
    assert '$second = Invoke-McpTool $Base $AgentToken "lan_result"' in text
    assert "Get-ClaimToken" in text
    assert '"bus.db"' in text


def test_script_checks_optional_properties_under_strict_mode():
    text = script_text()
    assert '$response.PSObject.Properties["error"]' in text
    assert '$created.PSObject.Properties["error"]' in text
    assert '$staleSubmit.PSObject.Properties["ignored"]' in text


def test_script_cancels_reworked_fake_task_before_terminal_case():
    text = script_text()
    second = text.index('$second.feedback.Contains')
    terminal = text.index("$terminal = New-FakeSubmitted")
    cancel = text.index('task_id = $normal.Id; reason = "state regression complete"')
    assert second < cancel < terminal


def test_recovery_uses_independent_workers_for_parallel_running_tasks():
    text = script_text()
    assert '[string]$RunDir, [string]$Name = ""' in text
    assert '"win-smoke-$HubKind-second"' in text
    assert '"win-smoke-$HubKind-replacement"' in text
    assert '$onceEnv = "$Env-recovery"' in text
