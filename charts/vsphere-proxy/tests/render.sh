#!/usr/bin/env bash
set -euo pipefail

chart="$(cd "$(dirname "$0")/.." && pwd)"
workdir="$(mktemp -d)"
trap 'rm -r "$workdir"' EXIT

render() {
  helm template example "$chart" \
    --set "targets.lab.endpoint=https://$1.example.invalid/sdk" \
    --set targets.lab.username_env=LAB_USERNAME \
    --set targets.lab.password_env=LAB_PASSWORD \
    --set 'existingSecrets[0]=vsphere-credentials' \
    --set existingCaConfigMap=vsphere-ca
}
render first > "$workdir/first.yaml"
render second > "$workdir/second.yaml"
helm template example "$chart" > "$workdir/default.yaml"

python3 - "$workdir" "$chart" <<'PY'
import json
import pathlib
import re
import subprocess
import sys

root = pathlib.Path(sys.argv[1])
first = (root / "first.yaml").read_text()
second = (root / "second.yaml").read_text()
default = (root / "default.yaml").read_text()

match = re.search(r'(?m)^  config\.json: \|\n((?:    .*\n)+)', first)
assert match, "rendered config is missing"
config = json.loads("\n".join(line[4:] for line in match.group(1).splitlines()))
assert config == {
    "listen": "0.0.0.0:8080",
    "targets": {"lab": {"endpoint": "https://first.example.invalid/sdk", "username_env": "LAB_USERNAME", "password_env": "LAB_PASSWORD", "insecure": False}},
}
assert 'type: ClusterIP' in first
assert 'name: "vsphere-credentials"' in first
assert 'name: "vsphere-ca"' in first
assert 'value: /etc/vsphere-proxy/ca/ca.crt' in first
assert 'envFrom:' not in default
assert 'SSL_CERT_FILE' not in default
hashes = [re.search(r'checksum/config: ([0-9a-f]{64})', text).group(1) for text in (first, second)]
assert hashes[0] != hashes[1], "target change must change the pod template checksum"
for extra in (
    "targets.lab.password=SYNTHETIC_PLACEHOLDER",
    "targets.lab.endpoint=https://user:SYNTHETIC_PLACEHOLDER@host.example.invalid/sdk",
    "targets.lab.endpoint=//user:SYNTHETIC_PLACEHOLDER@host.example.invalid/sdk",
    "targets.lab.endpoint=https://user:SYNTHETIC_PLACEHOLDER%40host.example.invalid/sdk",
    "targets.lab.endpoint=https://host.example.invalid/sdk?password=SYNTHETIC_PLACEHOLDER",
    "targets.lab.endpoint=https://host.example.invalid/sdk#token=SYNTHETIC_PLACEHOLDER",
):
    result = subprocess.run(
        ["helm", "template", "example", str(pathlib.Path(sys.argv[2])), "--set", extra],
        capture_output=True, text=True,
    )
    assert result.returncode != 0, "credentials in target configuration must be rejected"
    assert "SYNTHETIC_PLACEHOLDER" not in result.stdout + result.stderr, "error output exposed the input"
for endpoint in ("HTTPS://host.example.invalid/sdk", "https://[2001:db8::1]:443/sdk", "https://[fe80::1%25eth0]/sdk"):
    result = subprocess.run(
        ["helm", "template", "example", str(pathlib.Path(sys.argv[2])), "--set-string", "targets.lab.endpoint=" + endpoint],
        capture_output=True, text=True,
    )
    assert result.returncode == 0, "valid HTTPS target address was rejected"
result = subprocess.run(
    ["helm", "template", "example", str(pathlib.Path(sys.argv[2])), "--show-only", "templates/configmap.yaml",
     "--set", "targets.lab.endpoint=https://host.example.invalid/sdk", "--set", "targets.lab.insecure=true"],
    capture_output=True, text=True, check=True,
)
match = re.search(r'(?m)^  config\.json: \|\n((?:    .*\n)+)', result.stdout)
assert match
config = json.loads("\n".join(line[4:] for line in match.group(1).splitlines()))
assert config["targets"]["lab"]["insecure"] is True
print("Helm render checks passed")
PY
