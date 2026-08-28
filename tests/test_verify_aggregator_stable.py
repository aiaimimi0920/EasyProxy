import hashlib
import importlib.util
import json
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location(
    "verify_aggregator_stable",
    REPO_ROOT / "scripts" / "verify-aggregator-stable.py",
)
MODULE = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(MODULE)


class VerifyAggregatorStableTests(unittest.TestCase):
    def setUp(self):
        self.body = b"ss://stable\n"
        digest = hashlib.sha256(self.body).hexdigest()
        manifest = {
            "run_id": "run-42",
            "artifacts": [
                {
                    "name": "public-effective",
                    "stable_key": "subs/effective.txt",
                    "release_key": "releases/run-42/subs/effective.txt",
                    "sha256": digest,
                }
            ],
        }
        self.responses = {
            "https://sub.example.com/manifests/stable.json": json.dumps(manifest).encode("utf-8"),
            "https://sub.example.com/subs/effective.txt": self.body,
            "https://sub.example.com/releases/run-42/subs/effective.txt": self.body,
        }

    def fetch(self, url):
        return self.responses[url]

    def test_accepts_manifest_bound_stable_artifact(self):
        result = MODULE.verify_stable(
            "https://sub.example.com",
            "public-effective",
            "https://sub.example.com/subs/effective.txt",
            fetch=self.fetch,
        )
        self.assertEqual(result["run_id"], "run-42")
        self.assertEqual(result["stable_url"], "https://sub.example.com/subs/effective.txt")

    def test_rejects_noncanonical_consumer_url(self):
        with self.assertRaisesRegex(RuntimeError, "canonical stable artifact"):
            MODULE.verify_stable(
                "https://sub.example.com",
                "public-effective",
                "https://other.example.com/subs/effective.txt",
                fetch=self.fetch,
            )

    def test_rejects_stable_hash_drift(self):
        self.responses["https://sub.example.com/subs/effective.txt"] = b"ss://tampered\n"
        with self.assertRaisesRegex(RuntimeError, "stable artifact hash"):
            MODULE.verify_stable(
                "https://sub.example.com",
                "public-effective",
                "https://sub.example.com/subs/effective.txt",
                fetch=self.fetch,
            )


if __name__ == "__main__":
    unittest.main()
