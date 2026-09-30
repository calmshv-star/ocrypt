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

class FakeDocker:
    def __init__(self, failures=(), mismatch=None):
        self.containers = {"old-id": inspect_fixture()}
        self.failures = list(failures)
        self.calls = []
        self.mismatch = mismatch

    def fail_once(self, operation, identity, when):
        key = (operation, identity, when)
        if key in self.failures:
            self.failures.remove(key)
            raise RuntimeError("simulated lost Docker reply")

    def request(self, method, path, body=None):
        from urllib.parse import urlsplit, parse_qs
        parsed = urlsplit(path); parts = parsed.path.split('/'); query = parse_qs(parsed.query)
        self.calls.append((method, path))
        if path.startswith('/containers/create'):
            self.fail_once('create', 'new-id', 'before')
            self.containers['new-id'] = {'Id': 'new-id', 'Image': body['Image'], 'Name': '/'+query['name'][0], 'Config': copy.deepcopy(body), 'State': {'Running': False, 'Health': {'Status': 'healthy'}}}
            if self.mismatch == 'image':self.containers['new-id']['Image'] = 'sha256:unexpected'
            if self.mismatch == 'environment':self.containers['new-id']['Config']['Env'] = ['unexpected']
            self.fail_once('create', 'new-id', 'after')
            return {'Id': 'new-id'}
        identity = parts[2]
        item = self.containers.get(identity) or next((c for c in self.containers.values() if c['Name'].lstrip('/') == identity), None)
        if item is None:raise RuntimeError('Docker request failed: HTTP 404')
        if method == 'GET':return copy.deepcopy(item)
        operation = 'delete' if method == 'DELETE' else parts[3]
        identity = item['Id']
        self.fail_once(operation, identity, 'before')
        if operation == 'stop':item['State']['Running'] = False
        elif operation == 'start':item['State']['Running'] = True
        elif operation == 'rename':
            name = '/'+query['name'][0]
            if any(c['Id'] != identity and c['Name'] == name for c in self.containers.values()):raise RuntimeError('Docker request failed: HTTP 409')
            item['Name'] = name
        elif operation == 'delete':
            assert identity != 'old-id', 'original must remain recoverable'
            del self.containers[identity]
        else:raise AssertionError(operation)
        self.fail_once(operation, identity, 'after')
        return None


def test_apply_waits_for_exec_probe_and_retains_stopped_original():
    docker = FakeDocker()
    result = module.repair(docker, 'ocrypt-scanner-base', 'ocrypt-scanner-base-before-health-20260930', apply=True, wait_seconds=1)
    assert result['status'] == 'healthy'
    assert docker.containers['new-id']['State']['Running']
    assert docker.containers['new-id']['Config']['Healthcheck']['Test'] == ['CMD', '/app/service', '--check-ready']
    assert not docker.containers['old-id']['State']['Running']
    assert docker.containers['old-id']['Name'] == '/ocrypt-scanner-base-before-health-20260930'


@pytest.mark.parametrize('failures', [
    [('create','new-id','before')],
    [('start','new-id','before')],
    [('stop','old-id','after')],
    [('rename','old-id','after')],
    [('create','new-id','after')],
    [('start','new-id','before'),('stop','new-id','before')],
    [('start','new-id','before'),('delete','new-id','before')],
])
def test_failure_or_lost_reply_restores_original_from_daemon_state(failures):
    docker = FakeDocker(failures)
    with pytest.raises(RuntimeError, match='original scanner restored'):
        module.repair(docker, 'ocrypt-scanner-base', 'ocrypt-scanner-base-before-health-20260930', apply=True, wait_seconds=1)
    assert docker.containers['old-id']['State']['Running']
    assert docker.containers['old-id']['Name'] == '/ocrypt-scanner-base'
    assert all(not c['State']['Running'] for key,c in docker.containers.items() if key != 'old-id')


def test_failed_readiness_rolls_back_without_deleting_original():
    docker = FakeDocker()
    with pytest.raises(RuntimeError, match='original scanner restored'):
        module.repair(docker, 'ocrypt-scanner-base', 'ocrypt-scanner-base-before-health-20260930', apply=True, wait_seconds=0)
    assert docker.containers['old-id']['State']['Running']
    assert docker.containers['old-id']['Name'] == '/ocrypt-scanner-base'


@pytest.mark.parametrize('mismatch', ['image', 'environment'])
def test_detected_configuration_drift_still_cleans_known_created_id(mismatch):
    docker = FakeDocker(mismatch=mismatch)
    with pytest.raises(RuntimeError, match='original scanner restored'):
        module.repair(docker, 'ocrypt-scanner-base', 'ocrypt-scanner-base-before-health-20260930', apply=True, wait_seconds=1)
    assert docker.containers['old-id']['State']['Running']
    assert docker.containers['old-id']['Name'] == '/ocrypt-scanner-base'
    assert 'new-id' not in docker.containers
