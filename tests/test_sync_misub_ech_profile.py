import importlib.util
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[1]
SCRIPT_PATH = REPO_ROOT / "scripts" / "sync-misub-ech-profile.py"


spec = importlib.util.spec_from_file_location("sync_misub_ech_profile", SCRIPT_PATH)
sync_misub_ech_profile = importlib.util.module_from_spec(spec)
assert spec.loader is not None
spec.loader.exec_module(sync_misub_ech_profile)


class SyncMiSubEchProfileTests(unittest.TestCase):
    def test_attach_sources_preserves_other_connectors_and_replaces_managed_ech(self):
        profiles, missing = sync_misub_ech_profile.attach_sources_to_profiles(
            profiles=[
                {
                    "id": "aggregator_global",
                    "customId": "aggregator-global",
                    "manualNodes": [
                        "proxy_runtime_node_1",
                        "conn_zenproxy_primary",
                        "conn_ech_workers_pref_9",
                    ],
                }
            ],
            profile_ids=["aggregator-global"],
            source_ids=["conn_ech_workers_pref_1"],
            source_id_prefix="conn_ech_workers_pref",
        )

        self.assertEqual(missing, [])
        self.assertEqual(
            profiles[0]["manualNodes"],
            [
                "proxy_runtime_node_1",
                "conn_zenproxy_primary",
                "conn_ech_workers_pref_1",
            ],
        )

    def test_attach_sources_reports_missing_profile(self):
        profiles, missing = sync_misub_ech_profile.attach_sources_to_profiles(
            profiles=[],
            profile_ids=["aggregator-global"],
            source_ids=["conn_ech_workers_pref_1"],
            source_id_prefix="conn_ech_workers_pref",
        )

        self.assertEqual(profiles, [])
        self.assertEqual(missing, ["aggregator-global"])

    def test_validation_ignores_unmanaged_zenproxy_connector(self):
        sync_misub_ech_profile.validate_managed_connector_sources(
            sources=[
                {
                    "id": "conn_zenproxy_primary",
                    "kind": "connector",
                    "input": "https://zenproxy.example",
                    "options": {"connector_type": "zenproxy_client"},
                },
                {
                    "id": "conn_ech_workers_pref_1",
                    "kind": "connector",
                    "input": "https://ech.example:443",
                    "options": {
                        "connector_type": "ech_worker",
                        "connector_config": {"access_token": "ech-token"},
                    },
                },
            ],
            expected_source_ids=["conn_ech_workers_pref_1"],
            worker_url="https://ech.example:443",
            access_token="ech-token",
        )

    def test_validation_rejects_missing_managed_ech_source(self):
        with self.assertRaisesRegex(RuntimeError, "missing managed ECH sources"):
            sync_misub_ech_profile.validate_managed_connector_sources(
                sources=[
                    {
                        "id": "conn_zenproxy_primary",
                        "kind": "connector",
                        "input": "https://zenproxy.example",
                        "options": {"connector_type": "zenproxy_client"},
                    }
                ],
                expected_source_ids=["conn_ech_workers_pref_1"],
                worker_url="https://ech.example:443",
                access_token="ech-token",
            )


if __name__ == "__main__":
    unittest.main()
