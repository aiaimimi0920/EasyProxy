import importlib.util
import json
import unittest
from io import BytesIO
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[1]


def load_script(name: str, filename: str):
    spec = importlib.util.spec_from_file_location(name, REPO_ROOT / "scripts" / filename)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(module)
    return module


MATERIALIZE = load_script("materialize_aggregator_release", "materialize-aggregator-release.py")
PROMOTE = load_script("promote_aggregator_release", "promote-aggregator-release.py")


class FakeS3:
    def __init__(self):
        self.objects = {}
        self.fail_once_key = None

    def seed(self, bucket, key, body, content_type="text/plain; charset=utf-8", cache_control="max-age=60"):
        self.objects[(bucket, key)] = {
            "body": body if isinstance(body, bytes) else body.encode("utf-8"),
            "content_type": content_type,
            "cache_control": cache_control,
        }

    def get_object(self, Bucket, Key):
        item = self.objects.get((Bucket, Key))
        if item is None:
            raise KeyError(Key)
        return {
            "Body": BytesIO(item["body"]),
            "ContentType": item["content_type"],
            "CacheControl": item["cache_control"],
        }

    def put_object(self, Bucket, Key, Body, ContentType, CacheControl):
        if self.fail_once_key == Key:
            self.fail_once_key = None
            raise RuntimeError(f"injected failure for {Key}")
        self.seed(Bucket, Key, Body, ContentType, CacheControl)

    def delete_object(self, Bucket, Key):
        self.objects.pop((Bucket, Key), None)


class AggregatorReleaseTests(unittest.TestCase):
    def setUp(self):
        runtime = {
            "storage": {
                "engine": "r2",
                "items": {
                    "public-clash": {"bucket": "aggregator", "key": "subs/clash.yaml"},
                    "public-singbox": {"bucket": "aggregator", "key": "subs/singbox.json"},
                    "public-mixed": {"bucket": "aggregator", "key": "subs/mixed.txt"},
                    "public-effective": {"bucket": "aggregator", "key": "subs/effective.txt"},
                    "public-effective-json": {"bucket": "aggregator", "key": "subs/effective.json"},
                    "crawledsubs": {"bucket": "aggregator", "key": "internal/crawledsubs.json"},
                    "crawledproxies": {"bucket": "aggregator", "key": "internal/crawledproxies.txt"},
                },
            }
        }
        self.runtime, self.plan = MATERIALIZE.materialize_release(runtime, "run-42")
        self.client = FakeS3()
        self.candidate_bodies = {
            "public-clash": b"proxies:\n  - name: node\n",
            "public-singbox": b'{"outbounds":[{"tag":"node"}]}',
            "public-mixed": b"ss://candidate\n",
            "public-effective": b"ss://candidate\n",
            "public-effective-json": b'{"count":1,"stable_available_uris":["ss://candidate"]}',
            "crawledsubs": b'["https://source-a.example/sub","https://source-b.example/sub"]',
        }
        for item in self.plan["items"]:
            if item["publish"]:
                self.client.seed("aggregator", item["candidate_run_key"], self.candidate_bodies[item["name"]])
        self.audit = {"audit_id": "audit-42", "nodes": {"stable_available_uris": ["ss://candidate"]}}

    def public_fetch(self, _base_url, key, _run_id):
        return self.client.objects[("aggregator", key)]["body"]

    def promote(self, **overrides):
        kwargs = {
            "public_base_url": "https://sub.example.com",
            "root_commit": "root-sha",
            "aggregator_commit": "aggregator-sha",
            "min_nodes": 1,
            "min_sources": 1,
            "max_node_drop_ratio": 0.60,
            "max_source_drop_ratio": 0.80,
            "public_fetch": self.public_fetch,
        }
        kwargs.update(overrides)
        return PROMOTE.promote_release(self.client, self.plan, self.audit, **kwargs)

    def seed_previous_stable(self, nodes=1, sources=2):
        artifacts = []
        for item in self.plan["items"]:
            if item["publish"]:
                old_body = f"old-{item['name']}".encode("utf-8")
                old_release_key = f"releases/run-41/{item['stable_key']}"
                self.client.seed("aggregator", item["stable_key"], old_body)
                self.client.seed("aggregator", old_release_key, old_body)
                artifacts.append(
                    {
                        "name": item["name"],
                        "stable_key": item["stable_key"],
                        "release_key": old_release_key,
                        "sha256": PROMOTE.sha256_bytes(old_body),
                        "content_type": item["content_type"],
                    }
                )
        manifest = {
            "schema_version": 1,
            "run_id": "run-41",
            "counts": {"nodes": nodes, "sources": sources},
            "artifacts": artifacts,
        }
        body = PROMOTE.json_bytes(manifest)
        self.client.seed("aggregator", self.plan["stable_manifest_key"], body, PROMOTE.MANIFEST_CONTENT_TYPE)
        return body

    def test_materialize_rewrites_all_storage_keys_and_preserves_stable_plan(self):
        items = self.runtime["storage"]["items"]
        self.assertEqual(items["public-clash"]["key"], "candidate/run-42/subs/clash.yaml")
        planned = {item["name"]: item for item in self.plan["items"]}
        self.assertEqual(planned["public-clash"]["stable_key"], "subs/clash.yaml")
        self.assertEqual(planned["public-clash"]["release_key"], "releases/run-42/subs/clash.yaml")
        self.assertFalse(planned["crawledproxies"]["publish"])

    def test_empty_candidate_is_rejected_before_stable_changes(self):
        self.client.seed("aggregator", "candidate/run-42/subs/mixed.txt", b"")
        previous_manifest = self.seed_previous_stable()

        with self.assertRaisesRegex(RuntimeError, "candidate artifact is empty"):
            self.promote()

        self.assertEqual(
            self.client.objects[("aggregator", self.plan["stable_manifest_key"])]["body"],
            previous_manifest,
        )
        self.assertNotIn(("aggregator", "releases/run-42/subs/clash.yaml"), self.client.objects)

    def test_historical_drop_gate_rejects_candidate(self):
        previous_manifest = self.seed_previous_stable(nodes=10, sources=20)

        with self.assertRaisesRegex(RuntimeError, "node count 1 exceeds allowed drop"):
            self.promote()

        self.assertEqual(
            self.client.objects[("aggregator", self.plan["stable_manifest_key"])]["body"],
            previous_manifest,
        )

    def test_success_publishes_release_candidate_stable_and_lkg(self):
        previous_manifest = self.seed_previous_stable()

        manifest = self.promote()

        self.assertEqual(manifest["run_id"], "run-42")
        self.assertEqual(manifest["previous_run_id"], "run-41")
        self.assertEqual(manifest["counts"], {"nodes": 1, "sources": 2})
        for item in self.plan["items"]:
            if not item["publish"]:
                continue
            expected = self.candidate_bodies[item["name"]]
            self.assertEqual(self.client.objects[("aggregator", item["release_key"])]["body"], expected)
            self.assertEqual(self.client.objects[("aggregator", item["candidate_key"])]["body"], expected)
            self.assertEqual(self.client.objects[("aggregator", item["stable_key"])]["body"], expected)
            self.assertIn(("aggregator", f"last-known-good/releases/run-41/{item['stable_key']}"), self.client.objects)
        self.assertEqual(
            self.client.objects[("aggregator", self.plan["lkg_manifest_key"])]["body"],
            previous_manifest,
        )

    def test_failed_stable_write_restores_previous_objects_and_manifest(self):
        previous_manifest = self.seed_previous_stable()
        stable_before = {
            item["stable_key"]: self.client.objects[("aggregator", item["stable_key"])]["body"]
            for item in self.plan["items"]
            if item["publish"]
        }
        self.client.fail_once_key = "subs/singbox.json"

        with self.assertRaisesRegex(RuntimeError, "previous stable objects were restored"):
            self.promote()

        for key, body in stable_before.items():
            self.assertEqual(self.client.objects[("aggregator", key)]["body"], body)
        self.assertEqual(
            self.client.objects[("aggregator", self.plan["stable_manifest_key"])]["body"],
            previous_manifest,
        )

    def test_next_run_repairs_stable_drift_from_committed_release(self):
        previous_manifest = self.seed_previous_stable()
        old_clash = self.client.objects[("aggregator", "releases/run-41/subs/clash.yaml")]["body"]
        self.client.seed("aggregator", "subs/clash.yaml", b"partially-promoted")
        self.client.seed("aggregator", "candidate/run-42/subs/mixed.txt", b"")

        with self.assertRaisesRegex(RuntimeError, "candidate artifact is empty"):
            self.promote()

        self.assertEqual(self.client.objects[("aggregator", "subs/clash.yaml")]["body"], old_clash)
        self.assertEqual(
            self.client.objects[("aggregator", self.plan["stable_manifest_key"])]["body"],
            previous_manifest,
        )

    def test_tampered_plan_key_is_rejected(self):
        tampered = json.loads(json.dumps(self.plan))
        public_clash = next(item for item in tampered["items"] if item["name"] == "public-clash")
        public_clash["stable_key"] = "manifests/stable.json"

        with self.assertRaisesRegex(RuntimeError, "candidate_run_key is invalid"):
            PROMOTE.promote_release(
                self.client,
                tampered,
                self.audit,
                public_base_url="https://sub.example.com",
                root_commit="root-sha",
                aggregator_commit="aggregator-sha",
                min_nodes=1,
                min_sources=1,
                max_node_drop_ratio=0.60,
                max_source_drop_ratio=0.80,
                public_fetch=self.public_fetch,
            )

    def test_workflows_bind_publication_and_misub_to_stable_contract(self):
        aggregator_workflow = (REPO_ROOT / ".github/workflows/deploy-aggregator.yml").read_text(encoding="utf-8")
        cloudflare_workflow = (REPO_ROOT / ".github/workflows/deploy-cloudflare.yml").read_text(encoding="utf-8")

        self.assertIn("group: easyproxy-aggregator-publication", aggregator_workflow)
        self.assertIn("scripts/materialize-aggregator-release.py", aggregator_workflow)
        self.assertIn("scripts/promote-aggregator-release.py", aggregator_workflow)
        self.assertIn("scripts/verify-aggregator-stable.py", aggregator_workflow)
        self.assertIn("--subscription \"$baseUrl/candidate/$env:EASYPROXY_AGGREGATOR_RELEASE_ID/subs/clash.yaml\"", aggregator_workflow)
        self.assertIn("--runtime-config upstreams/aggregator/subscribe/config/config.actions.runtime.json", aggregator_workflow)
        self.assertIn("scripts/verify-aggregator-stable.py", cloudflare_workflow)
        self.assertIn("AGGREGATOR_PUBLIC_BASE_URL: ${{ vars.EASYPROXY_AGGREGATOR_PUBLIC_BASE_URL }}", cloudflare_workflow)


if __name__ == "__main__":
    unittest.main()
