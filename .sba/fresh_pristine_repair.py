"""准确新实例的空现场继续；只读检查，不清锁、移交或回放旧写入。"""
from coordination import r2_store
from d1_init import D1API, SCHEMA_QUERY, _schema
from domains import preflight_domains
from fresh_pristine_config import configuration_matches
from fresh_pristine_parent import verify_parent_receipt
from github_api import GitHubAPI
from pages_api import PagesAPI
from refresh_registry import REGISTRY_VARIABLE, parse_registry
from repair_context import FRESH_PRISTINE_CONTEXT as CONTEXT, FRESH_PRISTINE_PARENT_RELEASE as PARENT
from request import ApplicationRequest
from worker_api import WorkerAPI


class FreshPristineError(RuntimeError):
    pass


def require(value):
    if not value:
        raise FreshPristineError("EASYPROXY_SBA_FRESH_PRISTINE_REJECTED")


def assert_fresh_pristine(request, credentials):
    require(type(request) is ApplicationRequest and request.action == "repair"
            and request.previous == PARENT and request.context == CONTEXT
            and type(request.context.get("parentRunId")) is int and configuration_matches(request.config))
    verify_parent_receipt(credentials["EASYPROXY_GITHUB_TOKEN"])
    config = request.config
    store = r2_store(config.account_id, config.bucket, credentials["EASYPROXY_R2_ACCESS_KEY_ID"],
                     credentials["EASYPROXY_R2_SECRET_ACCESS_KEY"])
    try:
        require(store.read() is None and store.is_empty())
    finally:
        store.client.close()
    database = D1API(config.account_id, credentials["EASYPROXY_CLOUDFLARE_API_TOKEN"], config.database_id)
    try:
        identity = database.identity()
        require(identity.get("uuid") == config.database_id and identity.get("name") == config.database_name
                and not _schema(database.query(SCHEMA_QUERY)))
    finally:
        database.close()
    worker = WorkerAPI(config.account_id, credentials["EASYPROXY_CLOUDFLARE_API_TOKEN"])
    try:
        require(worker.settings_or_absent(config.worker_name) is None
                and worker.account_subdomain() == config.workers_subdomain)
    finally:
        worker.close()
    pages = PagesAPI(config.account_id, config.pages_name, credentials["EASYPROXY_CLOUDFLARE_API_TOKEN"])
    try:
        require(pages.project() is None)
    finally:
        pages.close()
    github = GitHubAPI(config.repository, config.environment, credentials["EASYPROXY_GITHUB_TOKEN"])
    try:
        github.repository_info()
        require(github.environment_info() is None)
        registry = github.repository_variable(REGISTRY_VARIABLE)
        require(registry is not None and parse_registry(registry, config.repository)["environments"] == [])
        github.active_workflow()
    finally:
        github.close()
    preflight_domains(config, credentials["EASYPROXY_CLOUDFLARE_API_TOKEN"], existing=False)
