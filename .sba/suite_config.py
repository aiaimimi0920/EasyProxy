"""将 SpringBok 的公开配置严格映射为同一套件身份；不读取秘密或调用 provider。"""
from __future__ import annotations

from dataclasses import dataclass
import re

from coordination import inventory_digest


CONFIG_KEYS = {"accountId", "repository", "pagesName", "workerName", "workersSubdomain", "databaseId",
               "databaseName", "bucketName", "refreshTarget", "pagesOrigin", "workerOrigin"}
WORKFLOW = ".github/workflows/sba-aggregator-refresh.yml"
NAME = re.compile(r"[a-z][a-z0-9-]{1,61}[a-z0-9]\Z")
REPOSITORY = re.compile(r"[A-Za-z0-9][A-Za-z0-9_.-]{0,99}/[A-Za-z0-9][A-Za-z0-9_.-]{0,99}\Z")


class ConfigurationError(RuntimeError):
    pass


def require(value):
    if not value:
        raise ConfigurationError("EASYPROXY_SBA_CONFIGURATION_REJECTED")


@dataclass(frozen=True)
class SuiteConfig:
    account_id: str
    repository: str
    environment: str
    pages_name: str
    worker_name: str
    workers_subdomain: str
    database_id: str
    database_name: str
    bucket: str
    refresh_target: str

    @property
    def pages_origin(self) -> str:
        return f"https://{self.pages_name}.pages.dev"

    @property
    def worker_origin(self) -> str:
        return f"https://{self.worker_name}.{self.workers_subdomain}.workers.dev"

    @property
    def inventory(self) -> list[dict]:
        return [{"kind": "d1", "name": self.database_name, "id": self.database_id},
                {"kind": "r2", "name": self.bucket, "id": self.bucket},
                {"kind": "pages", "name": self.pages_name, "id": self.pages_name},
                {"kind": "worker", "name": self.worker_name, "id": self.worker_name},
                {"kind": "github-actions", "name": self.refresh_target, "id": self.refresh_target}]

    @property
    def inventory_digest(self) -> str:
        return inventory_digest(self.inventory)

    def identity(self, purpose: str) -> dict:
        require(purpose in ("production", "preview"))
        return {"repository": self.repository, "environment": self.environment, "accountId": self.account_id,
                "bucket": self.bucket, "purpose": purpose}

    def configuration(self) -> dict:
        return {"accountId": self.account_id, "repository": self.repository, "pagesName": self.pages_name,
                "workerName": self.worker_name, "workersSubdomain": self.workers_subdomain,
                "databaseId": self.database_id, "databaseName": self.database_name, "bucketName": self.bucket,
                "refreshTarget": self.refresh_target, "pagesOrigin": self.pages_origin, "workerOrigin": self.worker_origin}

    @property
    def platform_inventory(self) -> list[dict]:
        # Keys/order mirror the application-owned deployment declaration, not provider discovery.
        rows = [("resource-database", "d1", self.database_id, self.database_name),
                ("resource-artifacts", "r2", self.bucket, self.bucket),
                ("target-0", "worker", self.worker_name, self.worker_name),
                ("target-1", "pages", self.pages_name, self.pages_name),
                ("target-2", "github-actions", self.refresh_target, self.refresh_target),
                ("target-3", "domain", self.worker_origin, self.worker_origin),
                ("target-4", "domain", self.pages_origin, self.pages_origin)]
        return [{"key": key, "kind": kind, "accountId": "github:" + self.repository if kind == "github-actions"
                 else self.account_id, "remoteId": identity, "name": name} for key, kind, identity, name in rows]


def parse_configuration(value: dict, *, repository: str, environment: str) -> SuiteConfig:
    require(type(value) is dict and set(value) == CONFIG_KEYS
            and all(type(item) is str for item in value.values())
            and isinstance(repository, str) and REPOSITORY.fullmatch(repository) is not None
            and isinstance(environment, str) and re.fullmatch(r"[a-z][a-z0-9-]{1,62}", environment) is not None)
    require(REPOSITORY.fullmatch(value["repository"]) is not None
            and value["repository"].lower() == repository.lower()
            and re.fullmatch(r"[a-f0-9]{32}", value["accountId"]) is not None
            and re.fullmatch(r"[a-f0-9]{8}(?:-[a-f0-9]{4}){3}-[a-f0-9]{12}", value["databaseId"]) is not None
            and re.fullmatch(r"[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?", value["workersSubdomain"]) is not None)
    expected = {"pagesName": f"ep-{environment}-misub", "workerName": f"ep-{environment}-edge",
                "databaseName": f"ep-{environment}-misub", "bucketName": f"ep-{environment}-store"}
    require(all(NAME.fullmatch(value[key]) is not None and value[key] == name for key, name in expected.items())
            and value["refreshTarget"] == WORKFLOW + "@" + environment
            and value["pagesOrigin"] == f"https://{value['pagesName']}.pages.dev"
            and value["workerOrigin"] == f"https://{value['workerName']}.{value['workersSubdomain']}.workers.dev")
    return SuiteConfig(value["accountId"], repository.lower(), environment, value["pagesName"], value["workerName"],
                       value["workersSubdomain"], value["databaseId"], value["databaseName"], value["bucketName"],
                       value["refreshTarget"])
