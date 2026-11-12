from pathlib import Path


ROOT = Path(__file__).resolve().parent.parent


def test_release_workflow_builds_advertised_installers_with_test_gate():
    workflow = (ROOT / ".github" / "workflows" / "release.yml").read_text(encoding="utf-8")
    assert "go test ./..." in workflow
    assert "python -m pytest tests -q" in workflow
    assert "__version__" in workflow
    assert "packaging/macos/build-pkg.sh" in workflow
    assert "packaging/linux/build-deb.sh" in workflow
    assert r"packaging\windows\lanbus.iss" in workflow
    assert "needs: [test, cli, gui, wheel, package-macos, package-linux]" in workflow
    for artifact in (
        "lanbus-${version}-setup.exe",
        "lanbus-${version}-windows-amd64.zip",
        "lanbus-${version}-macos-arm64.pkg",
        "lanbus_${version}_amd64.deb",
    ):
        assert f'test -f "release/{artifact}"' in workflow


def test_windows_portable_launcher_uses_native_cli():
    script = (ROOT / "scripts" / "package_windows.py").read_text(encoding="utf-8")
    assert '"%~dp0bin\\lanbus.exe" %*' in script
    assert "python -m lanbus %*" not in script


def test_python_package_uses_single_version_source():
    pyproject = (ROOT / "pyproject.toml").read_text(encoding="utf-8")
    assert 'dynamic = ["version"]' in pyproject
    assert "[tool.setuptools.dynamic]" in pyproject
    assert 'version = {attr = "lanbus.__version__"}' in pyproject
    assert '\nversion = "1.9.0"\n' not in pyproject
