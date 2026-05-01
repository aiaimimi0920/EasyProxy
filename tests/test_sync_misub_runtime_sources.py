import importlib.util
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[1]
SCRIPT_PATH = REPO_ROOT / "scripts" / "sync-misub-runtime-sources.py"


spec = importlib.util.spec_from_file_location("sync_misub_runtime_sources", SCRIPT_PATH)
sync_misub_runtime_sources = importlib.util.module_from_spec(spec)
assert spec.loader is not None
spec.loader.exec_module(sync_misub_runtime_sources)


class SyncMiSubRuntimeSourcesTests(unittest.TestCase):
    def test_resolve_connector_node_ids_prefers_configured_connector_ids(self):
        result = sync_misub_runtime_sources.resolve_connector_node_ids(
            settings={
                "aggregatorSync": {
                    "defaultPublicProfileConnectorIds": [
                        "conn_zenproxy_primary",
                        "invalid-source",
                    ]
                }
            },
            existing_profile={
                "manualNodes": ["conn_ech_workers_pref_1"]
            },
            sources=[
                {
                    "id": "conn_zenproxy_primary",
                    "kind": "connector",
                },
                {
                    "id": "conn_ech_workers_pref_1",
                    "kind": "connector",
                },
                {
                    "id": "invalid-source",
                    "kind": "proxy_uri",
                },
            ],
        )

        self.assertEqual(result, ["conn_zenproxy_primary"])

    def test_resolve_connector_node_ids_falls_back_to_existing_profile_connectors(self):
        result = sync_misub_runtime_sources.resolve_connector_node_ids(
            settings={"aggregatorSync": {}},
            existing_profile={
                "manualNodes": [
                    "proxy_runtime_node_1",
                    "conn_ech_workers_pref_1",
                    "user-direct-node",
                ]
            },
            sources=[
                {
                    "id": "proxy_runtime_node_1",
                    "kind": "proxy_uri",
                    "options": {"managed_by": "easyproxy_runtime_sources"},
                },
                {
                    "id": "conn_ech_workers_pref_1",
                    "kind": "connector",
                },
                {
                    "id": "user-direct-node",
                    "kind": "proxy_uri",
                },
            ],
        )

        self.assertEqual(result, ["conn_ech_workers_pref_1"])

    def test_resolve_connector_node_ids_returns_empty_when_no_connectors_exist(self):
        result = sync_misub_runtime_sources.resolve_connector_node_ids(
            settings={},
            existing_profile={"manualNodes": ["proxy_runtime_node_1"]},
            sources=[
                {
                    "id": "proxy_runtime_node_1",
                    "kind": "proxy_uri",
                }
            ],
        )

        self.assertEqual(result, [])


if __name__ == "__main__":
    unittest.main()
