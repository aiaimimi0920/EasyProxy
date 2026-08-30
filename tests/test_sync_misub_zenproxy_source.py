import base64
import importlib.util
import json
from pathlib import Path

from cryptography.hazmat.primitives import hashes
from cryptography.hazmat.primitives.ciphers.aead import AESGCM
from cryptography.hazmat.primitives.kdf.pbkdf2 import PBKDF2HMAC


SCRIPT = Path(__file__).parents[1] / "scripts" / "sync-misub-zenproxy-source.py"
SPEC = importlib.util.spec_from_file_location("sync_misub_zenproxy_source", SCRIPT)
MODULE = importlib.util.module_from_spec(SPEC)
assert SPEC and SPEC.loader
SPEC.loader.exec_module(MODULE)


def test_update_zen_source_preserves_secret_and_other_sources():
    sources = [
        {"id": "other", "input": "https://example.com"},
        {
            "id": "conn_zenproxy_primary",
            "options": {
                "connector_type": "zenproxy_client",
                "connector_config": {"api_key": "secret", "count": 5, "type": ""},
            },
        },
    ]

    updated = MODULE.update_zen_source(sources, "conn_zenproxy_primary", 10, "http", True)

    assert sources[1]["options"]["connector_config"]["type"] == ""
    assert updated[0] == sources[0]
    source = updated[1]
    assert source["connector_config"]["api_key"] == "secret"
    assert source["connector_config"]["count"] == 10
    assert source["connector_config"]["type"] == "http"
    assert source["connector_config"]["chatgpt"] is True
    assert source["options"]["connector_config"] == source["connector_config"]


def test_encrypt_backup_matches_browser_envelope_contract():
    payload = {"format": "misub-logical-backup", "data": {"sources": [], "profiles": []}}
    envelope = MODULE.encrypt_backup(payload, "backup-passphrase")
    salt = base64.b64decode(envelope["kdf"]["salt"])
    iv = base64.b64decode(envelope["cipher"]["iv"])
    key = PBKDF2HMAC(
        algorithm=hashes.SHA256(),
        length=32,
        salt=salt,
        iterations=MODULE.BACKUP_ITERATIONS,
    ).derive(b"backup-passphrase")
    plaintext = AESGCM(key).decrypt(
        iv,
        base64.b64decode(envelope["ciphertext"]),
        MODULE.BACKUP_AAD,
    )

    assert json.loads(plaintext) == payload
