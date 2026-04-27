import base64
import json
import sys
import unittest
from pathlib import Path
from unittest.mock import patch


BASE_DIR = Path(__file__).resolve().parents[1]
SUBSCRIBE_DIR = BASE_DIR / "subscribe"

if str(SUBSCRIBE_DIR) not in sys.path:
    sys.path.insert(0, str(SUBSCRIBE_DIR))

import airport  # noqa: E402
import crawl  # noqa: E402
import mailtm  # noqa: E402
import renewal  # noqa: E402
import workflow  # noqa: E402


class DummyResponse:
    def __init__(self, code=200, body=b"", headers=None):
        self._code = code
        self._body = body
        self._headers = headers or {}

    def getcode(self):
        return self._code

    def read(self):
        return self._body

    def getheader(self, name, default=None):
        return self._headers.get(name, default)


class WorkflowRegressionTests(unittest.TestCase):
    def test_liveness_filter_handles_none(self):
        checks, nochecks = workflow.liveness_fillter(None)
        self.assertEqual(checks, [])
        self.assertEqual(nochecks, [])

    def test_liveness_filter_splits_live_and_static_nodes(self):
        proxies = [
            {"name": "a", "liveness": True, "sub": "https://a.example"},
            {"name": "b", "liveness": False, "sub": "https://b.example", "chatgpt": True},
        ]

        checks, nochecks = workflow.liveness_fillter(proxies)

        self.assertEqual(len(checks), 1)
        self.assertEqual(checks[0]["name"], "a")
        self.assertEqual(len(nochecks), 1)
        self.assertEqual(nochecks[0]["name"], "b")
        self.assertNotIn("sub", nochecks[0])
        self.assertNotIn("chatgpt", nochecks[0])


class RenewalRegressionTests(unittest.TestCase):
    @patch("renewal.utils.http_get")
    def test_get_free_plan_prefers_highest_traffic_free_plan(self, mock_http_get):
        mock_http_get.return_value = """
        {
          "data": [
            {"id": 1, "month_price": 0, "renew": 1, "reset_price": 0, "transfer_enable": 100},
            {"id": 2, "month_price": 0, "renew": 1, "reset_price": 0, "transfer_enable": 500}
          ]
        }
        """

        plan = renewal.get_free_plan(
            domain="https://example.com",
            cookies="session=1",
            authorization="",
            retry=1,
        )

        self.assertIsNotNone(plan)
        self.assertEqual(plan.plan_id, 2)
        self.assertEqual(plan.trafficflow, 500)

    @patch("renewal.submit_ticket", return_value=True)
    @patch("renewal.flow", return_value=True)
    @patch("renewal.get_payment_method", return_value=[1])
    @patch("renewal.get_subscribe_info")
    @patch("renewal.get_cookies", return_value=("session=1", "auth"))
    def test_add_traffic_flow_does_not_mutate_ticket_config(
        self,
        mock_get_cookies,
        mock_get_subscribe_info,
        mock_get_payment_method,
        mock_flow,
        mock_submit_ticket,
    ):
        mock_get_subscribe_info.return_value = renewal.SubscribeInfo(
            plan_id=3,
            renew_enable=True,
            reset_enable=True,
            used_rate=0.9,
            expired_days=1,
            package="month_price",
            sub_url="https://example.com/api/v1/client/subscribe?token=abc123",
            reset_day=0,
        )

        ticket = {
            "enable": True,
            "autoreset": False,
            "subject": "Need reset",
            "message": "Please help",
            "level": 1,
        }
        params = {
            "email": base64.b64encode(b"user@example.com").decode(),
            "passwd": base64.b64encode(b"secret").decode(),
            "ticket": ticket,
        }

        sub_url = renewal.add_traffic_flow("https://example.com", params)

        self.assertEqual(
            sub_url,
            "https://example.com/api/v1/client/subscribe?token=abc123",
        )
        self.assertIn("enable", ticket)
        self.assertIn("autoreset", ticket)
        self.assertEqual(ticket["subject"], "Need reset")
        self.assertEqual(mock_flow.call_count, 2)
        mock_submit_ticket.assert_called_once()
        mock_get_payment_method.assert_called_once()
        mock_get_cookies.assert_called_once()


class MailTMRegressionTests(unittest.TestCase):
    @patch("mailtm.utils.http_get", return_value="{")
    def test_mailtm_get_messages_handles_invalid_json(self, mock_http_get):
        client = mailtm.MailTM()
        client.auth_headers = {"Authorization": "Bearer token"}
        account = mailtm.Account(address="user@example.com", password="secret", id="1")

        messages = client.get_messages(account)

        self.assertEqual(messages, [])
        mock_http_get.assert_called_once()


class CrawlRegressionTests(unittest.TestCase):
    def test_extract_subscribes_managed_config_link_is_detected(self):
        content = '#!MANAGED-CONFIG https://example.com/api/v1/client/subscribe?token=ABCDEFGHIJKLMNOP'

        results = crawl.extract_subscribes(content=content, source="TEST")

        self.assertIn(
            "https://example.com/api/v1/client/subscribe?token=ABCDEFGHIJKLMNOP",
            results,
        )
        self.assertEqual(
            results["https://example.com/api/v1/client/subscribe?token=ABCDEFGHIJKLMNOP"]["origin"],
            "TEST",
        )

    def test_extract_subscribes_does_not_reuse_default_push_list_between_calls(self):
        content = "https://example.com/api/v1/client/subscribe?token=ABCDEFGHIJKLMNOP"

        first = crawl.extract_subscribes(content=content)
        first[content]["push_to"].append("mutated")
        second = crawl.extract_subscribes(content=content)

        self.assertEqual(second[content]["push_to"], [])

    @patch("crawl.utils.http_get", return_value='<link rel="canonical" href="/s/demo?before=12345">')
    def test_get_telegram_pages_parses_before_marker(self, mock_http_get):
        before = crawl.get_telegram_pages("demo")

        self.assertEqual(before, 12345)
        mock_http_get.assert_called_once()


class AirportRegressionTests(unittest.TestCase):
    @patch("airport.utils.http_get")
    def test_get_register_require_normalizes_whitelist(self, mock_http_get):
        mock_http_get.return_value = json.dumps(
            {
                "data": {
                    "is_email_verify": 1,
                    "is_invite_force": 0,
                    "is_recaptcha": 1,
                    "email_whitelist_suffix": None,
                }
            }
        )

        result = airport.AirPort.get_register_require("https://example.com")

        self.assertTrue(result.verify)
        self.assertFalse(result.invite)
        self.assertTrue(result.recaptcha)
        self.assertEqual(result.whitelist, [])

    @patch("airport.utils.http_get", return_value="{")
    def test_get_register_require_falls_back_to_defaults_on_invalid_json(self, mock_http_get):
        result = airport.AirPort.get_register_require(
            "https://example.com",
            default=False,
        )

        self.assertFalse(result.verify)
        self.assertFalse(result.invite)
        self.assertFalse(result.recaptcha)
        self.assertEqual(result.whitelist, [])

    @patch.object(airport.AirPort, "order_plan", return_value=True)
    @patch("airport.renewal.get_subscribe_info", return_value=None)
    @patch("airport.urllib.request.urlopen")
    def test_register_falls_back_to_token_when_subscribe_info_missing(
        self,
        mock_urlopen,
        mock_get_subscribe_info,
        mock_order_plan,
    ):
        body = json.dumps({"data": {"token": "abc123token"}}).encode("utf8")
        mock_urlopen.return_value = DummyResponse(
            code=200,
            body=body,
            headers={"Set-Cookie": "v2board_session=session123; Path=/;"},
        )

        client = airport.AirPort(name="demo", site="https://example.com", sub="")
        cookies, authorization = client.register(
            email="user@example.com",
            password="secret123",
            retry=1,
        )

        self.assertEqual(cookies, "v2board_session=session123;")
        self.assertEqual(authorization, "")
        self.assertEqual(
            client.sub,
            "https://example.com/api/v1/client/subscribe?token=abc123token",
        )
        mock_order_plan.assert_called_once()
        mock_get_subscribe_info.assert_called_once()

    @patch("airport.urllib.request.urlopen")
    def test_fetch_unused_filters_high_rate_nodes(self, mock_urlopen):
        body = json.dumps(
            {
                "data": [
                    {"name": "low", "rate": "1.0"},
                    {"name": "high", "rate": "5.0"},
                ]
            }
        ).encode("utf8")
        mock_urlopen.return_value = DummyResponse(code=200, body=body)

        client = airport.AirPort(name="demo", site="https://example.com", sub="")
        client.fetch = "https://example.com/api/v1/user/server/fetch"

        result = client.fetch_unused("session=1", rate=3.0)

        self.assertEqual(result, ["high"])


if __name__ == "__main__":
    unittest.main()
