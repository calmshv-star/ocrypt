import copy
import importlib.util
from pathlib import Path
import pytest

SPEC = importlib.util.spec_from_file_location("scanner_healthcheck_repair", Path(__file__).resolve().parents[2] / "scripts/repair_scanner_healthcheck.py")
module = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(module)

def inspect_fixture():
    return {"Id": "old-id", "Image": "sha256:immutable", "Name": "/ocrypt-scanner-base", "State": {"Running": True},
            "Config": {"Image": "ocrypt-scanner:accepted", "Env": ["DATABASE_URL=test-secret-never-display", "SCANNER_QUORUM=2"], "Entrypoint": ["/app/service"], "Cmd": [], "User": "65532:65532", "Healthcheck": {"Test": ["CMD-SHELL", "/app/service --check-ready"], "Interval": 60000000000, "Timeout": 5000000000, "Retries": 2}},
            "HostConfig": {"NetworkMode": "ocrypt-data", "ReadonlyRootfs": True, "CapDrop": ["ALL"], "RestartPolicy": {"Name": "unless-stopped", "MaximumRetryCount": 0}, "Memory": 512000000},
            "NetworkSettings": {"Networks": {"ocrypt-data": {"Aliases": ["ocrypt-scanner-base"], "IPAMConfig": None, "IPAddress": "172.27.0.22"}}}}

def test_repair_changes_only_probe_transport_and_pins_existing_image():
    original = inspect_fixture(); saved = copy.deepcopy(original)
    plan = module.repair_plan(original)
    assert original == saved
    assert plan["Image"] == original["Image"]
    expected = copy.deepcopy(original["Config"]); expected["Image"] = original["Image"]
    expected["Healthcheck"]["Test"] = ["CMD", "/app/service", "--check-ready"]
    assert {k:v for k,v in plan.items() if k not in ("HostConfig", "NetworkingConfig")} == expected
    assert plan["HostConfig"] == original["HostConfig"]
    endpoint = plan["NetworkingConfig"]["EndpointsConfig"]["ocrypt-data"]
    assert endpoint == {"Aliases": ["ocrypt-scanner-base"]}

@pytest.mark.parametrize("name", ["/ocrypt-api", "/postgres", "/showy-scanner-base"])
def test_non_scanner_cannot_be_recreated(name):
    original = inspect_fixture();original["Name"] = name
    with pytest.raises(ValueError):module.repair_plan(original)

@pytest.mark.parametrize("probe", [["CMD-SHELL", "curl [endpoint]"], ["CMD-SHELL", "/app/service --check-ready; echo extra"], ["CMD", "/app/service", "--check-ready"]])
def test_unknown_or_already_correct_probe_is_not_recreated(probe):
    original = inspect_fixture();original["Config"]["Healthcheck"]["Test"] = probe
    with pytest.raises(ValueError):module.repair_plan(original)

def test_mutation_requires_explicit_apply():
    class NoMutation:
        def request(self, method, path, body=None):
            assert method == "GET"
            return inspect_fixture()
    result = module.repair(NoMutation(), "ocrypt-scanner-base", "ocrypt-scanner-base-before-health-20260930", apply=False)
    assert result["status"] == "dry-run"
    assert "test-secret-never-display" not in str(result)
