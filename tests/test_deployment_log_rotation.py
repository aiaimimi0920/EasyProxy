from pathlib import Path

import pytest
import yaml


ROOT = Path(__file__).parents[1]


@pytest.mark.parametrize("layout", ["gateway/debian", "nas", "service/base"])
def test_official_compose_layouts_bound_container_logs(layout):
    document = yaml.safe_load((ROOT / "deploy" / layout / "docker-compose.yaml").read_text(encoding="utf-8"))
    service = next(iter(document["services"].values()))
    assert service["logging"] == {
        "driver": "json-file",
        "options": {
            "max-size": "${EASY_DOCKER_LOG_MAX_SIZE:-20m}",
            "max-file": "${EASY_DOCKER_LOG_MAX_FILE:-5}",
        },
    }
