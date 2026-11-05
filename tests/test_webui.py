"""Web UI smoke tests: HTML is public, APIs require the agent token."""
import json
import urllib.error
import urllib.request

from bus import TOKEN


def _get(url, token=None):
    headers = {"Authorization": "Bearer " + token} if token else {}
    req = urllib.request.Request(url, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=10) as r:
            return r.status, r.headers.get("Content-Type", ""), r.read()
    except urllib.error.HTTPError as e:
        return e.code, e.headers.get("Content-Type", ""), e.read()


def test_ui_is_public_and_env_api_needs_token(hub):
    status, ctype, body = _get(hub.url + "/ui")
    assert status == 200 and "text/html" in ctype
    assert b"lanbus" in body and b"/api/envs" in body

    status, ctype, _ = _get(hub.url + "/")
    assert status == 200 and "text/html" in ctype

    status, ctype, _ = _get(hub.url + "/api/envs")
    assert status == 401

    status, ctype, body = _get(hub.url + "/api/envs", TOKEN)
    assert status == 200 and "application/json" in ctype
    data = json.loads(body)
    assert "workers" in data and "envs" in data
