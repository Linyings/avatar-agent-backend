"""Verify the live Docker deployment without printing credentials or tokens."""
import json
from pathlib import Path
import urllib.error
import urllib.request

base = "http://127.0.0.1:8080"
root = Path("/opt/aicat/avatar-mvp")
def parse_access_accounts(text):
    accounts, current = [], {}
    for line in text.splitlines():
        if ": " not in line:
            continue
        key, value = line.split(": ", 1)
        if key == "Email":
            if current.get("email") and current.get("password"):
                accounts.append(current)
            current = {"email": value}
        elif key == "Password":
            current["password"] = value
    if current.get("email") and current.get("password"):
        accounts.append(current)
    return accounts


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


def login_saved_account(accounts):
    for account in accounts:
        status, login = request("/api/v1/auth/login", account)
        if status == 401:
            continue
        if status != 200:
            raise RuntimeError(f"Deployment login check failed: HTTP {status}")
        return login.get("data", login)["tokens"]["access_token"]
    raise RuntimeError("No saved deployment account can log in; update LOCAL_ACCESS.txt.")


def main():
    status, _ = request("/api-health")
    assert status == 200, status
    status, _ = request("/api/v1/agent/history")
    assert status == 401, status
    token = login_saved_account(parse_access_accounts((root / "LOCAL_ACCESS.txt").read_text()))
    status, _ = request("/api/v1/auth/me", token=token)
    assert status == 200, status
    # Missing project id reaches the new Agent handler (400), not a missing route
    # (404). This read-only check never starts a model request or workflow.
    status, _ = request("/api/v1/agent/history", token=token)
    assert status == 400, status
    status, config = request("/api/v1/avatar-mvp/config", token=token)
    assert status == 200, status
    assert config.get("data", config)["enabled"] is True
    print(json.dumps({"health": "passed", "unauthorized": "rejected", "login": "passed",
                      "profile": "passed", "agent_routes": "passed", "legacy_config": "passed",
                      "entry": base + "/"}, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
