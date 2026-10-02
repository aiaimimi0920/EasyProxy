import importlib.util
import json
import os
import subprocess
import textwrap
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
            directory.return_value.name = '/test-audit'
            directory.return_value.cleanup.side_effect = PermissionError('Docker-owned directory')
            run.return_value = Mock(returncode=0)
            result = sync_misub_runtime_sources.run_runtime_audit(
                audit_script=Path('audit.py'), subscriptions=[], docker_network_name='audit',
                image='test-image', scenario_timeout_seconds=60,
            )
            directory.assert_called_once_with(prefix='easyproxy-misub-audit-', ignore_cleanup_errors=True)
            self.assertEqual(result, {'nodes': {'stable_available_uris': []}})
            directory.return_value.cleanup.assert_called_once()
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


    def test_management_origin_normalizes_url_boundaries(self):
        cases = {
            "https://EXAMPLE.invalid": "https://example.invalid",
            "https://EXAMPLE.invalid:443/": "https://example.invalid",
            "http://EXAMPLE.invalid:80/prefix/": "http://example.invalid",
            "https://example.invalid:8443/path?key=test#fragment": "https://example.invalid:8443",
            "http://example.invalid:443/": "http://example.invalid:443",
            "https://user:synthetic-password@EXAMPLE.invalid:443/path?token=synthetic#fragment": "https://example.invalid",
            "https://[2001:0db8:0:0:0:0:0:1]:443/prefix/": "https://[2001:db8::1]",
            "http://[::1]:8080/": "http://[::1]:8080",
            "https://b\u00fccher.example/": "https://xn--bcher-kva.example",
            "https://fa\u00df.example:443/": "https://xn--fa-hia.example",
            "https://\u03c2.example:8443/path": "https://xn--3xa.example:8443",
        }
        for base_url, expected in cases.items():
            with self.subTest(base_url=base_url):
                self.assertEqual(sync_misub_runtime_sources.management_origin(base_url), expected)

    def test_management_origin_rejects_invalid_urls_without_echoing_them(self):
        for value in ("", "//example.invalid", "ftp://example.invalid", "https:///path",
                      "https://example.invalid:65536", "https://example.invalid:bad",
                      "https://[broken", "https://exa\nmple.invalid", "https://bad host.invalid",
                      "https://bad\\host.invalid", "https://exa%6dple.invalid"):
            with self.subTest(value=value):
                with self.assertRaisesRegex(RuntimeError, "^MiSub base URL must be a valid HTTP\\(S\\) URL$"):
                    sync_misub_runtime_sources.management_origin(value)

    def test_cookie_management_posts_match_real_misub_csrf_contract(self):
        for base_url in ("https://misub.example:443/", "http://misub.example:80/prefix/",
                         "https://fa\u00df.example:443/", "https://\u03c2.example:8443/prefix/"):
            with self.subTest(base_url=base_url):
                session = sync_misub_runtime_sources.requests.Session()
                recorded = []
                saved_sources = []

                def send(request, **kwargs):
                    # Intercept every prepared request; no transport or audit runs.
                    recorded.append(request)
                    path = sync_misub_runtime_sources.urlsplit(request.url).path
                    if path.endswith("/api/login"):
                        session.cookies.set("misub_session", "synthetic-session")
                        payload = {"success": True}
                    elif path.endswith("/api/data"):
                        payload = {"misubs": [], "profiles": []}
                    elif path.endswith("/api/settings"):
                        payload = {"success": True} if request.method == "POST" else {"aggregatorSync": {}}
                    elif path.endswith("/api/misubs"):
                        saved_sources.extend(json.loads(request.body)["misubs"])
                        payload = {"success": True}
                    elif "/api/manifest/" in path:
                        payload = {"success": True, "sources": saved_sources}
                    else:
                        self.fail("unexpected request path")
                    response = sync_misub_runtime_sources.requests.Response()
                    response.status_code = 200
                    response._content = json.dumps(payload).encode()
                    response.request = request
                    response.url = request.url
                    return response

                with patch.object(sync_misub_runtime_sources.requests, "Session", return_value=session), \
                     patch.object(session, "send", side_effect=send), \
                     patch.object(sync_misub_runtime_sources, "run_runtime_audit", return_value={
                         "audit_id": "offline-contract", "nodes": {"stable_available_uris": ["http://proxy.invalid:80"]},
                     }), \
                     patch.dict(os.environ, {
                         "MISUB_ADMIN_PASSWORD": "synthetic-admin",
                         "MISUB_MANIFEST_TOKEN": "synthetic-manifest",
                         "MISUB_CRON_SECRET": "synthetic-cron",
                     }, clear=True), \
                     patch("sys.argv", ["sync", "--base-url", base_url, "--subscription-url", "https://source.invalid/sub"]), \
                     patch("builtins.print"):
                    self.assertEqual(sync_misub_runtime_sources.main(), 0)

                writes = [request for request in recorded
                          if request.method == "POST" and not request.url.endswith("/api/login")]
                self.assertEqual(len(writes), 2)
                self.assertEqual([sync_misub_runtime_sources.urlsplit(request.url).path.rsplit("/", 1)[1]
                                  for request in writes], ["misubs", "settings"])
                origin = sync_misub_runtime_sources.management_origin(base_url)
                cases = []
                for request in writes:
                    prepared_base = sync_misub_runtime_sources.requests.Request("GET", base_url).prepare().url
                    self.assertTrue(request.url.startswith(prepared_base))
                    self.assertEqual(request.headers["Origin"], origin)
                    self.assertIn("misub_session=synthetic-session", request.headers["Cookie"])
                    self.assertNotIn("Authorization", request.headers)
                    headers = {"Cookie": request.headers["Cookie"]}
                    cases.extend([
                        {"url": request.url, "headers": headers, "status": 403, "called": False, "body": "Origin Required"},
                        {"url": request.url, "headers": {**headers, "Origin": "https://wrong.invalid"},
                         "status": 403, "called": False, "body": "Origin Not Allowed"},
                        {"url": request.url, "headers": {**headers, "Origin": request.headers["Origin"]},
                         "status": 200, "called": True, "body": "next"},
                    ])
                manifest = next(request for request in recorded if "/api/manifest/" in request.url)
                self.assertEqual(manifest.headers["Authorization"], "Bearer synthetic-manifest")
                self.assertNotIn("Origin", manifest.headers)
                self.assertTrue(all("Origin" not in request.headers for request in recorded if request not in writes))
                middleware = REPO_ROOT / "upstreams/misub/functions/middleware/cors.js"
                result = subprocess.run(
                    ["node", "--input-type=module", "-e", textwrap.dedent("""
                        import { pathToFileURL } from 'node:url';
                        import { readFileSync } from 'node:fs';
                        import assert from 'node:assert/strict';
                        const { csrfOriginMiddleware } = await import(pathToFileURL(process.argv[1]).href);
                        for (const item of JSON.parse(readFileSync(0, 'utf8'))) {
                            let called = false;
                            const response = await csrfOriginMiddleware(
                                new Request(item.url, { method: 'POST', headers: item.headers }),
                                async () => { called = true; return new Response('next'); }
                            );
                            assert.equal(response.status, item.status);
                            assert.equal(called, item.called);
                            assert.equal(await response.text(), item.body);
                        }
                    """), str(middleware)],
                    input=json.dumps(cases), text=True, capture_output=True, check=False,
                )
                self.assertEqual(result.returncode, 0, result.stderr)


if __name__ == "__main__":
    unittest.main()
