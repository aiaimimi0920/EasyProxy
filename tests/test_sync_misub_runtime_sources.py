import importlib.util
import unittest
from unittest.mock import Mock, patch
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[1]
SCRIPT_PATH = REPO_ROOT / "scripts" / "sync-misub-runtime-sources.py"


spec = importlib.util.spec_from_file_location("sync_misub_runtime_sources", SCRIPT_PATH)
sync_misub_runtime_sources = importlib.util.module_from_spec(spec)
assert spec.loader is not None
spec.loader.exec_module(sync_misub_runtime_sources)


class SyncMiSubRuntimeSourcesTests(unittest.TestCase):
    def test_runtime_audit_cleanup_is_best_effort_but_result_is_required(self):
        with patch.object(sync_misub_runtime_sources.tempfile, 'TemporaryDirectory') as directory, \
             patch.object(sync_misub_runtime_sources.subprocess, 'run') as run, \
             patch.object(Path, 'read_text', return_value='{"nodes":{"stable_available_uris":[]}}'):
            directory.return_value.__enter__.return_value = '/test-audit'
            run.return_value = Mock(returncode=0)
            result = sync_misub_runtime_sources.run_runtime_audit(
                audit_script=Path('audit.py'), subscriptions=[], docker_network_name='audit',
                image='test-image', scenario_timeout_seconds=60,
            )
            directory.assert_called_once_with(prefix='easyproxy-misub-audit-', ignore_cleanup_errors=True)
            self.assertEqual(result, {'nodes': {'stable_available_uris': []}})
            run.return_value = Mock(returncode=1, stderr='probe failed', stdout='')
            with self.assertRaisesRegex(RuntimeError, 'probe failed'):
                sync_misub_runtime_sources.run_runtime_audit(
                    audit_script=Path('audit.py'), subscriptions=[], docker_network_name='audit',
                    image='test-image', scenario_timeout_seconds=60,
                )

    def test_resolve_connector_node_ids_preserves_existing_and_configured_connectors(self):
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

        self.assertEqual(result, ["conn_zenproxy_primary", "conn_ech_workers_pref_1"])

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
