"""Verify the live Docker deployment without printing credentials or tokens."""
import json
from pathlib import Path
import urllib.error
import urllib.request

base = "http://127.0.0.1:8080"
root = Path("/opt/aicat/avatar-mvp")
access = {}
for line in (root / "LOCAL_ACCESS.txt").read_text().splitlines():
    if ": " not in line:
        continue
    key, value = line.split(": ", 1)
    access.setdefault(key, value)


def request(path, data=None, token=None):
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = f"Bearer {token}"
    req = urllib.request.Request(base + path, headers=headers,
                                 data=json.dumps(data).encode() if data is not None else None)
    try:
        with urllib.request.urlopen(req, timeout=20) as res:
            return res.status, json.load(res)
    except urllib.error.HTTPError as err:
        return err.code, json.load(err)


status, _ = request("/api-health")
assert status == 200, status
status, _ = request("/api/v1/avatar-mvp/config")
assert status == 401, status
status, login = request("/api/v1/auth/login", {"email": access["Email"], "password": access["Password"]})
assert status == 200, (status, login)
data = login.get("data", login)
token = data["tokens"]["access_token"]
status, _ = request("/api/v1/auth/me", token=token)
assert status == 200, status
status, config = request("/api/v1/avatar-mvp/config", token=token)
assert status == 200, status
data = config.get("data", config)
assert data["enabled"] is True
print(json.dumps({"health": "passed", "unauthorized": "rejected", "login": "passed",
                  "profile": "passed", "avatar_config": data}, ensure_ascii=False, indent=2))
