"""同一许可内的自定义域名绑定；先证明归属，写入单次发送，UNKNOWN 不重放。"""
from __future__ import annotations

from dataclasses import dataclass
import json
import re
import time

from cloudflare_http import api_result, secure_session
from coordination import ETAG, StoreConflict, StoreUnknown
from origins import https_origin
from worker_api import credential


class DomainError(RuntimeError):
    pass


def require(value, code="REJECTED"):
    if not value:
        raise DomainError("EASYPROXY_SBA_DOMAIN_" + code)


def _id(value, *, worker=False):
    pattern = r"[a-f0-9]{40}" if worker else r"[a-f0-9]{32}|[a-f0-9]{8}(?:-[a-f0-9]{4}){3}-[a-f0-9]{12}"
    require(type(value) is str and re.fullmatch(pattern, value), "IDENTITY")
    return value


@dataclass(frozen=True)
class DomainTarget:
    kind: str
    service: str
    origin: str

    def __post_init__(self):
        require(self.kind in ("worker", "pages") and type(self.service) is str
            and re.fullmatch(r"[a-z][a-z0-9-]{1,61}[a-z0-9]", self.service)
            and https_origin(self.origin) and self.origin[8:].count(".") >= 2
            and not self.origin.endswith((".pages.dev", ".workers.dev")), "IDENTITY")

    @property
    def hostname(self):
        return self.origin[8:]


def targets(config):
    return [DomainTarget(kind, service, origin) for kind, service, origin in (
        ("worker", config.worker_name, config.custom_worker_origin),
        ("pages", config.pages_name, config.custom_pages_origin)) if origin is not None]


class DomainAPI:
    def __init__(self, account, token):
        require(type(account) is str and re.fullmatch(r"[a-f0-9]{32}", account)
                and credential(token), "CREDENTIALS")
        self.account = account
        self.token = token
        self.session = secure_session()

    def close(self):
        self.session.close()

    def _request(self, method, path, **kwargs):
        worker_domain = path == f"/accounts/{self.account}/workers/domains"
        pages_domain = "/pages/projects/" in path and "/domains" in path
        return api_result(self.session, self.token, method, path, require,
                          worker_domain_response=worker_domain, pages_domain_response=pages_domain, **kwargs)

    def zone(self, target):
        # Match the selector contract: exactly one label under an active zone.
        zone_name = target.hostname.split(".", 1)[1]
        rows = self._request("GET", "/zones", params={"account.id": self.account,
            "name": zone_name, "per_page": 50, "page": 1})
        require(type(rows) is list and len(rows) == 1, "ZONE")
        zone = rows[0]
        require(type(zone) is dict and zone.get("name") == zone_name
                and zone.get("status") == "active" and type(zone.get("account")) is dict
                and zone["account"].get("id") == self.account, "ZONE")
        return {"id": _id(zone.get("id")), "name": zone_name}

    def dns(self, target, zone):
        rows = self._request("GET", f"/zones/{zone['id']}/dns_records",
                             params={"name": target.hostname, "per_page": 100, "page": 1})
        require(type(rows) is list and len(rows) < 100 and all(type(row) is dict
                and row.get("name") == target.hostname for row in rows), "DNS")
        return rows

    def worker_domains(self, target):
        rows = self._request("GET", f"/accounts/{self.account}/workers/domains",
                             params={"hostname": target.hostname})
        require(type(rows) is list and len(rows) <= 1 and all(type(row) is dict
                and row.get("hostname") == target.hostname for row in rows), "ASSOCIATION")
        return rows

    def pages_associations(self, target):
        matches, seen = [], set()
        for page in range(1, 101):
            rows = self._request("GET", f"/accounts/{self.account}/pages/projects",
                                 params={"per_page": 10, "page": page})
            require(type(rows) is list and len(rows) <= 10, "ASSOCIATION")
            for row in rows:
                require(type(row) is dict and type(row.get("name")) is str
                        and row["name"] not in seen and type(row.get("domains")) is list
                        and all(type(value) is str for value in row["domains"]), "ASSOCIATION")
                seen.add(row["name"])
                if target.hostname in row["domains"]:
                    matches.append(row["name"])
            if len(rows) < 10:
                require(len(matches) <= 1, "ASSOCIATION")
                return matches
        require(False, "ASSOCIATION")

    def _worker(self, row, target, zone, *, attach_response=False):
        # Script-based PUT can return "" for this deprecated field. GET must
        # still prove canonical production ownership before durable evidence.
        require(type(attach_response) is bool and type(row) is dict and row.get("hostname") == target.hostname
                and row.get("service") == target.service and row.get("zone_id") == zone["id"]
                and row.get("zone_name") == zone["name"]
                and (row.get("environment") == "production" or attach_response and row.get("environment") == "")
                and row.get("enabled") is True and row.get("previews_enabled") is False, "OWNERSHIP")
        return _id(row.get("id"), worker=True)

    def _pages(self, row, target, zone):
        require(type(row) is dict and row.get("name") == target.hostname
                and row.get("zone_tag") == zone["id"] and row.get("status") in
                    ("initializing", "pending", "active"), "OWNERSHIP")
        return _id(row.get("id"))

    def _dns(self, rows, target):
        require(len(rows) == 1, "OWNERSHIP")
        row = rows[0]
        kind, content = ("AAAA", "100::") if target.kind == "worker" else ("CNAME", target.service + ".pages.dev")
        require(row.get("type") == kind and row.get("content") == content
                and row.get("proxied") is True, "OWNERSHIP")
        return _id(row.get("id"))

    def inspect(self, target, *, existing):
        zone = self.zone(target)
        dns, workers, pages = self.dns(target, zone), self.worker_domains(target), self.pages_associations(target)
        if not existing:
            require(not dns and not workers and not pages, "UNOWNED_RESOURCE")
            return zone
        self._dns(dns, target)
        if target.kind == "worker":
            require(len(workers) == 1 and not pages, "OWNERSHIP")
            self._worker(workers[0], target, zone)
        else:
            require(not workers and pages == [target.service], "OWNERSHIP")
            row = self._request("GET", f"/accounts/{self.account}/pages/projects/{target.service}/domains/{target.hostname}")
            self._pages(row, target, zone)
            require(row["status"] == "active", "NOT_READY")
        return zone

    def bind(self, owner, permit, target):
        require(permit.action in ("deploy", "update") and owner.identity["purpose"] == "production", "OWNERSHIP")
        existing = permit.action == "update"
        zone = self.inspect(target, existing=existing)
        owner.assert_owned(permit)
        if existing:
            return
        owner.claim_stage(permit, target.kind + "-custom-domain")
        if target.kind == "worker":
            result = self._request("PUT", f"/accounts/{self.account}/workers/domains", json={
                "hostname": target.hostname, "service": target.service, "zone_id": zone["id"],
                "enabled": True, "previews_enabled": False})
            identity = self._worker(result, target, zone, attach_response=True)
        else:
            path = f"/accounts/{self.account}/pages/projects/{target.service}/domains"
            result = self._request("POST", path, json={"name": target.hostname})
            identity = self._pages(result, target, zone)
            owner.assert_owned(permit)
            records = self.dns(target, zone)
            # Some provider paths create the exact CNAME. Verify, never overwrite.
            if not records:
                owner.claim_stage(permit, "pages-custom-domain-dns")
                record = self._request("POST", f"/zones/{zone['id']}/dns_records", json={
                    "type": "CNAME", "name": target.hostname, "content": target.service + ".pages.dev",
                    "proxied": True, "ttl": 1, "comment": "EasyProxy SBA " + owner.inventory_digest})
                require(type(record) is dict and record.get("name") == target.hostname, "DNS")
                self._dns([record], target)
            else:
                self._dns(records, target)
        owner.assert_owned(permit)
        # Only GETs wait for DNS/certificate propagation. Never resend a write.
        for attempt in range(120):
            if target.kind == "worker":
                rows = self.worker_domains(target)
                require(len(rows) == 1 and self._worker(rows[0], target, zone) == identity, "OWNERSHIP")
                ready = True
            else:
                row = self._request("GET", path + "/" + target.hostname)
                require(self._pages(row, target, zone) == identity, "OWNERSHIP")
                ready = row["status"] == "active"
            records = self.dns(target, zone)
            if ready and records:
                dns_id = self._dns(records, target)
                break
            owner.assert_owned(permit)
            if attempt < 119:
                time.sleep(5)
        else:
            require(False, "NOT_READY")
        self.inspect(target, existing=True)
        owner.assert_owned(permit)
        evidence = {"schemaVersion": 1, "accountId": self.account, "inventoryDigest": owner.inventory_digest,
            "kind": target.kind, "service": target.service, "hostname": target.hostname,
            "zoneId": zone["id"], "domainId": identity, "dnsId": dns_id}
        body = json.dumps(evidence, sort_keys=True, separators=(",", ":")).encode("utf-8")
        try:
            etag = owner.store.create_stage(permit.operation_id, target.kind + "-domain-owned", body)
        except (StoreConflict, StoreUnknown):
            raise DomainError("EASYPROXY_SBA_DOMAIN_EVIDENCE_UNKNOWN") from None
        require(type(etag) is str and ETAG.fullmatch(etag), "EVIDENCE_UNKNOWN")
        owner.assert_owned(permit)


def preflight_domains(config, token, *, existing):
    selected = targets(config)
    if not selected:
        return
    api = DomainAPI(config.account_id, token)
    try:
        for target in selected:
            api.inspect(target, existing=existing)
    finally:
        api.close()


def bind_domain(owner, permit, inventory, service, kind, token):
    rows = [row for row in inventory if row.get("kind") == "domain" and row.get("name") == service]
    if not rows:
        return
    require(len(rows) == 1, "IDENTITY")
    api = DomainAPI(owner.identity["accountId"], token)
    try:
        api.bind(owner, permit, DomainTarget(kind, service, rows[0]["id"]))
    finally:
        api.close()
