"""真实HTTP合成provider：上传存储与近期登记分离，不要求未保证的即时读屏障。"""
import json

import pytest

from diagnostics import StageError, safe_error_code
from test_pages import (candidate, api_wire, held, publish, api_module, ARTIFACT, IDENTITY, PROJECT,
                        state_writer)


@pytest.mark.parametrize("cached", [False, True])
def test_registration_lag_does_not_block_single_deployment_or_reupload_cached_assets(candidate,api_wire,cached):
    release,settings=candidate
    owner,permit,store,_=held(release)
    api_wire["mode"]="registration-lag"
    if cached:
        api_wire["assets"]={a.hash for a in ARTIFACT.assets}
        api_wire["registered"]=set(api_wire["assets"])
    assert publish(owner,permit,settings)["checks"]["pages-published"] is True
    paths=[path for _,path,*_ in api_wire["writes"]]
    assert sum(path.endswith("check-missing") for path in paths)==1
    assert sum(path.endswith("upsert-hashes") for path in paths)==1
    assert sum(path.endswith("/upload") for path in paths)==int(not cached)
    assert paths.index(next(p for p in paths if p.endswith("upsert-hashes"))) < paths.index(next(p for p in paths if p.endswith("/deployments")))
    assert api_wire["registered"]=={a.hash for a in ARTIFACT.assets} and api_wire["deployments"]==1
    assert state_writer(store)==permit.operation_id and json.loads(store.body)["active"] is None


@pytest.mark.parametrize("guard,stage",[(1,"QUERY"),(2,"UPLOAD"),(3,"REGISTER")])
def test_owner_read_still_gates_every_actual_operation(api_wire,guard,stage):
    api=api_module.PagesAPI(IDENTITY["accountId"],PROJECT,"fixture-api-token")
    calls=[]
    def before_write():
        calls.append(True)
        if len(calls)==guard: raise OSError("PRIVATE_OWNER_ERROR")
    try:
        with pytest.raises(StageError) as error: api.upload_assets(ARTIFACT.assets,before_write)
        assert safe_error_code(error.value)==f"EASYPROXY_SBA_PAGES_ASSET_{stage}_UNKNOWN"
        assert len(api_wire["requests"])==guard and api_wire["deployments"]==0
        assert not any(path.endswith("upsert-hashes") for _,path in api_wire["requests"])
        assert "PRIVATE" not in str(error.value)
    finally: api.close()


@pytest.mark.parametrize("result",[None,{},True,1,"hash",["f"*32],["a"*32]*2,[True]])
def test_first_query_validation_still_rejects_bad_shape_without_asset_writes(api_wire,result):
    api_wire["queryResult"]=result
    api=api_module.PagesAPI(IDENTITY["accountId"],PROJECT,"fixture-api-token")
    try:
        with pytest.raises(StageError): api.upload_assets(ARTIFACT.assets,lambda: None)
        assert len(api_wire["requests"])==2 and api_wire["assets"]==set()
        assert api_wire["deployments"]==0
    finally: api.close()
