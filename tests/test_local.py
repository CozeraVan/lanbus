"""Local worker supervisor: config init and dry-run command construction."""
import json
import os

from lanbus import local


def test_local_init_and_dry_run(tmp_path):
    path = str(tmp_path / "local.json")
    assert local.command(["init", "--config", path]) == 0
    cfg = json.load(open(path, encoding="utf-8"))
    assert cfg["workers"]
    cfg["hub"] = "http://127.0.0.1:1"
    for w in cfg["workers"]:
        w["binary"] = "/tmp/fake-lanbus-worker"
        w["cwd"] = str(tmp_path)
    local.write_config(cfg, path)
    assert local.command(["status", "--config", path]) == 0
    assert local.command(["up", "--config", path, "--dry-run"]) == 0
    assert local.command(["down", "--config", path, "--dry-run"]) == 0
