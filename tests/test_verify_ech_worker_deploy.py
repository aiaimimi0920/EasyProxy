import importlib.util
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[1]
SCRIPT_PATH = REPO_ROOT / "scripts" / "verify-ech-worker-deploy.py"
spec = importlib.util.spec_from_file_location("verify_ech_worker_deploy", SCRIPT_PATH)
verifier = importlib.util.module_from_spec(spec)
assert spec.loader is not None
spec.loader.exec_module(verifier)


class VerifyECHWorkerDeployTests(unittest.TestCase):
    def test_build_dns_tcp_query_frames_a_standard_a_question(self):
        framed, transaction_id = verifier.build_dns_tcp_query(
            "example.com",
            transaction_id=b"EP",
        )

        self.assertEqual(transaction_id, b"EP")
        self.assertEqual(int.from_bytes(framed[:2], "big"), len(framed) - 2)
        self.assertEqual(framed[2:4], b"EP")
        self.assertEqual(framed[4:6], b"\x01\x00")
        self.assertTrue(framed.endswith(b"\x00\x00\x01\x00\x01"))

    def test_validate_dns_tcp_response_accepts_matching_noerror_response(self):
        query, transaction_id = verifier.build_dns_tcp_query(
            "example.com",
            transaction_id=b"EP",
        )
        packet = transaction_id + b"\x81\x80" + query[6:]
        response = len(packet).to_bytes(2, "big") + packet

        verifier.validate_dns_tcp_response(response, transaction_id)

    def test_validate_dns_tcp_response_rejects_mismatched_transaction(self):
        query, _ = verifier.build_dns_tcp_query("example.com", transaction_id=b"EP")
        packet = b"NO" + b"\x81\x80" + query[6:]
        response = len(packet).to_bytes(2, "big") + packet

        with self.assertRaisesRegex(RuntimeError, "mismatched transaction"):
            verifier.validate_dns_tcp_response(response, b"EP")


if __name__ == "__main__":
    unittest.main()
