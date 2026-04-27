import base64
import json
import os
import pathlib
import sys
import urllib.error
import urllib.request

from nacl import encoding, public


API_VERSION = "2026-03-10"
API_ROOT = "https://api.github.com"
PLACEHOLDER_MARKERS = (
    "__KEY_PLACEHOLDER__",
    "__TOKEN_PLACEHOLDER__",
    "PLACEHOLDER",
)


def getenv(name: str) -> str:
    value = os.environ.get(name, "").strip()
    if not value:
        raise RuntimeError(f"Missing required environment variable: {name}")
    return value


def api_request(token: str, method: str, url: str, payload: dict | None = None) -> dict | None:
    headers = {
        "Accept": "application/vnd.github+json",
        "Authorization": f"Bearer {token}",
        "X-GitHub-Api-Version": API_VERSION,
        "User-Agent": "easyproxy-aggregator-dispatch",
    }
    data = None
    if payload is not None:
        data = json.dumps(payload).encode("utf-8")
        headers["Content-Type"] = "application/json"

    request = urllib.request.Request(url, data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(request) as response:
            body = response.read()
            if not body:
                return None
            return json.loads(body.decode("utf-8"))
    except urllib.error.HTTPError as exc:
        body = exc.read().decode("utf-8", errors="replace")
        raise RuntimeError(f"GitHub API {method} {url} failed: {exc.code} {body}") from exc


def encrypt_secret(secret_value: str, public_key_b64: str) -> str:
    public_key = public.PublicKey(public_key_b64.encode("utf-8"), encoding.Base64Encoder())
    sealed_box = public.SealedBox(public_key)
    encrypted = sealed_box.encrypt(secret_value.encode("utf-8"))
    return base64.b64encode(encrypted).decode("utf-8")


def append_summary(lines: list[str]) -> None:
    summary_path = os.environ.get("GITHUB_STEP_SUMMARY", "").strip()
    if not summary_path:
        return
    with open(summary_path, "a", encoding="utf-8") as handle:
        handle.write("\n".join(lines) + "\n")


def main() -> int:
    token = getenv("AGGREGATOR_REPO_TOKEN")
    repo = getenv("AGGREGATOR_REPO")
    workflow = getenv("AGGREGATOR_WORKFLOW")
    ref = getenv("AGGREGATOR_REF")
    secret_name = getenv("AGGREGATOR_SECRET_NAME")
    config_path = pathlib.Path(getenv("AGGREGATOR_CONFIG_PATH"))

    if "/" not in repo:
        raise RuntimeError(f"Invalid AGGREGATOR_REPO value: {repo}")
    owner, repo_name = repo.split("/", 1)

    if not config_path.is_file():
        raise RuntimeError(f"Aggregator source config not found: {config_path}")

    config_content = config_path.read_text(encoding="utf-8")
    if any(marker in config_content for marker in PLACEHOLDER_MARKERS):
        raise RuntimeError(
            f"Aggregator source config still contains placeholder values: {config_path}"
        )

    encoded_config = base64.b64encode(config_content.encode("utf-8")).decode("utf-8")
    public_key = api_request(
        token,
        "GET",
        f"{API_ROOT}/repos/{owner}/{repo_name}/actions/secrets/public-key",
    )
    encrypted_value = encrypt_secret(encoded_config, public_key["key"])

    api_request(
        token,
        "PUT",
        f"{API_ROOT}/repos/{owner}/{repo_name}/actions/secrets/{secret_name}",
        {
            "encrypted_value": encrypted_value,
            "key_id": public_key["key_id"],
        },
    )

    api_request(
        token,
        "POST",
        f"{API_ROOT}/repos/{owner}/{repo_name}/actions/workflows/{workflow}/dispatches",
        {"ref": ref},
    )

    append_summary(
        [
            "## Aggregator workflow dispatched",
            "",
            f"- repository: `{repo}`",
            f"- workflow: `{workflow}`",
            f"- ref: `{ref}`",
            f"- updated secret: `{secret_name}`",
            f"- source config: `{config_path.as_posix()}`",
        ]
    )
    print(f"Updated {secret_name} and dispatched {workflow} on {repo}@{ref}")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:  # pragma: no cover - CLI failure path
        print(str(exc), file=sys.stderr)
        raise SystemExit(1)
