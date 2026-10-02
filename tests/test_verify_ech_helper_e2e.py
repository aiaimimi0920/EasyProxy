import importlib.util
import unittest
from unittest.mock import Mock
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[1]
SCRIPT_PATH = REPO_ROOT / "scripts" / "verify-ech-helper-e2e.py"
spec = importlib.util.spec_from_file_location("verify_ech_helper_e2e", SCRIPT_PATH)
verify_ech_helper_e2e = importlib.util.module_from_spec(spec)
assert spec.loader is not None
spec.loader.exec_module(verify_ech_helper_e2e)


class VerifyECHHelperE2ETests(unittest.TestCase):
    def test_http_probe_rejects_proxy_error_instead_of_accepting_any_http_header(self):
        connection = Mock()
        connection.recv.return_value = b"HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n"
        with self.assertRaisesRegex(RuntimeError, "status 502"):
            verify_ech_helper_e2e.read_http_response(connection)

    def test_http_probe_accepts_complete_success_response(self):
        connection = Mock()
        connection.recv.side_effect = [b"HTTP/1.1 204 No Content\r\n", b"Connection: close\r\n\r\n"]
        self.assertIn(b"204", verify_ech_helper_e2e.read_http_response(connection))

    def test_http_probe_rejects_truncated_headers(self):
        connection = Mock()
        connection.recv.side_effect = [b"HTTP/1.1 200 OK\r\n", b""]
        with self.assertRaisesRegex(RuntimeError, "incomplete HTTP headers"):
            verify_ech_helper_e2e.read_http_response(connection)

    def test_worker_address_normalizes_urls(self):
        self.assertEqual(verify_ech_helper_e2e.worker_address("https://worker.example"), "worker.example:443")
        self.assertEqual(verify_ech_helper_e2e.worker_address("worker.example:8443"), "worker.example:8443")

    def test_worker_address_rejects_paths(self):
        with self.assertRaisesRegex(RuntimeError, "must not contain a path"):
            verify_ech_helper_e2e.worker_address("https://worker.example/tunnel")

    def test_worker_address_rejects_non_http_schemes(self):
        with self.assertRaisesRegex(RuntimeError, "must use http or https"):
            verify_ech_helper_e2e.worker_address("ftp://worker.example")


if __name__ == "__main__":
    unittest.main()
