import importlib.util
import unittest
from ipaddress import ip_address
from pathlib import Path
from unittest.mock import patch
import json


ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location(
    "validate_tun_gateway_docker", ROOT / "scripts/validate-tun-gateway-docker.py"
)
gateway = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gateway)


class GatewayInterfaceAddressTests(unittest.TestCase):
    def check_addresses(self, attachments, addresses):
        with patch.object(gateway, "docker", return_value=json.dumps(addresses)) as docker, \
             patch("builtins.print"):
            gateway.assert_gateway_interfaces("test-gateway", attachments)
        docker.assert_called_once_with(
            "exec", "test-gateway", "ip", "-j", "address", "show", capture=True
        )

    @staticmethod
    def interface(name, *addresses):
        return {"ifname": name, "addr_info": [{"local": value} for value in addresses]}

    def test_all_subnet_blocks_accept_kernel_compression_in_both_attachment_orders(self):
        for block in range(14):
            host = block * 16
            attachments = [
                ("client", f"192.0.2.{host + 2}", f"2001:db8:1:{block:x}::2", "lan0"),
                ("origin", f"203.0.113.{host + 3}", f"2001:db8:2:{block:x}::3", "wan0"),
            ]
            addresses = [
                self.interface(name, ipv4, str(ip_address(ipv6)), "fe80::1")
                for _, ipv4, ipv6, name in attachments
            ]
            for ordered in (attachments, list(reversed(attachments))):
                with self.subTest(block=block, first=ordered[0][0]):
                    self.check_addresses(ordered, addresses)

    def test_expanded_uppercase_ipv6_is_equivalent(self):
        self.check_addresses(
            [("origin", "203.0.113.3", "2001:db8:2:0::3", "wan0")],
            [self.interface("wan0", "203.0.113.3", "2001:0DB8:0002:0000:0000:0000:0000:0003")],
        )

    def test_different_or_missing_addresses_are_rejected(self):
        attachment = [("origin", "203.0.113.3", "2001:db8:2:0::3", "wan0")]
        for actual in [
            ("203.0.113.3", "2001:db8:2::4"),
            ("203.0.113.4", "2001:db8:2::3"),
            ("2001:db8:2::3",),
            ("203.0.113.3",),
            ("::ffff:203.0.113.3", "2001:db8:2::3"),
        ]:
            with self.subTest(actual=actual), self.assertRaises(RuntimeError):
                self.check_addresses(attachment, [self.interface("wan0", *actual)])

    def test_matching_addresses_on_wrong_interface_are_rejected(self):
        with self.assertRaises(RuntimeError):
            self.check_addresses(
                [("origin", "203.0.113.3", "2001:db8:2:0::3", "wan0")],
                [self.interface("lan0", "203.0.113.3", "2001:db8:2::3")],
            )

    def test_invalid_address_is_rejected(self):
        with self.assertRaises(ValueError):
            self.check_addresses(
                [("origin", "203.0.113.3", "2001:db8:2::3", "wan0")],
                [self.interface("wan0", "invalid-ip", "2001:db8:2::3")],
            )


if __name__ == "__main__":
    unittest.main()
