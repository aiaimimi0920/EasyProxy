"""准确 WRITER_UNKNOWN 后的只读门禁；不改父回执、不接管任何 writer。"""
import hashlib

from aggregator_publish import PublicArtifacts, _previous
from artifact_store import ArtifactStore
from coordination import Coordinator, r2_store
from fresh_pristine_config import configuration_matches
from repair_context import WRITER_IDLE_CONTEXT as CONTEXT, WRITER_IDLE_PARENT_RELEASE as PARENT
from repair_context import WRITER_IDLE_ACTIVE_RELEASE as ACTIVE
from request import ApplicationRequest
from writer_idle_parent import verify_parent_receipt

RECOVERED_OPERATION = "e4f663ee8ce14c6ba271141bb58a17c3"
RECOVERY_SHA256 = "108387ff3de693a1ff115ec3908320388e197ebd01b4f89e5bcd90635ccf3048"


def require(value):
    if not value:
        raise RuntimeError("EASYPROXY_SBA_WRITER_IDLE_REJECTED")


def assert_writer_idle(request, credentials):
    require(type(request) is ApplicationRequest and request.action == "repair"
            and request.context == CONTEXT and request.previous == PARENT
            and request.application_version == "1.0.23" and configuration_matches(request.config))
    verify_parent_receipt(credentials["EASYPROXY_GITHUB_TOKEN"])
    config = request.config
    store = r2_store(config.account_id, config.bucket, credentials["EASYPROXY_R2_ACCESS_KEY_ID"],
                     credentials["EASYPROXY_R2_SECRET_ACCESS_KEY"])
    public = PublicArtifacts(config.worker_origin + "/aggregator", inventory=config.inventory)
    try:
        before = store.read()
        owner = Coordinator(store, config.identity("production"), config.inventory_digest)
        require(owner.current_release() == ACTIVE)
        response = store.client.get_object(Bucket=store.bucket,
            Key=f"_sba/operations/{RECOVERED_OPERATION}/operator-refresh-recovery.json")
        try:
            evidence = response["Body"].read(16385)
        finally:
            response["Body"].close()
        require(len(evidence) <= 16384 and hashlib.sha256(evidence).hexdigest() == RECOVERY_SHA256)
        prior, manifest, artifacts = _previous(ArtifactStore(store), public, ACTIVE)
        require(prior is not None and len(artifacts) == 7)
        require(store.read() == before)
    finally:
        public.close()
        store.client.close()
