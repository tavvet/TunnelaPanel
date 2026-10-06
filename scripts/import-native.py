#!/usr/bin/env python3
"""Copy a native installation's config for a staged container; never edit source."""
import argparse
import json
import os
from pathlib import Path
import re
import shutil
import tempfile
import tomllib


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", type=Path, default=Path("/opt/trusttunnel"))
    parser.add_argument("--target", type=Path, required=True)
    args = parser.parse_args()
    source, target = args.source.resolve(), args.target.resolve()
    if target.exists():
        raise SystemExit("Target already exists; refusing to overwrite configuration.")
    vpn_text = (source / "vpn.toml").read_text()
    vpn = tomllib.loads(vpn_text)
    hosts = tomllib.loads((source / "hosts.toml").read_text())
    if "quic" in vpn.get("listen_protocols", {}):
        raise SystemExit("QUIC is enabled; review UDP ports before using this TCP-only deployment.")
    if vpn.get("credentials_file") not in ("credentials.toml", str(source / "credentials.toml")):
        raise SystemExit("Custom credentials path requires explicit migration review.")
    if vpn.get("rules_file") not in (None, "rules.toml", str(source / "rules.toml")):
        raise SystemExit("Custom rules path requires explicit migration review.")
    if "metrics" in vpn:
        raise SystemExit("Existing metrics settings require explicit migration review.")
    for entries in hosts.values():
        if not isinstance(entries, list):
            continue
        for host in entries:
            for key in ("cert_chain_path", "private_key_path"):
                cert_path = Path(host.get(key, ""))
                if not str(cert_path).startswith("/etc/letsencrypt/") or not cert_path.is_file():
                    raise SystemExit("Certificate must exist under the mounted /etc/letsencrypt tree.")
    credentials = (source / "credentials.toml").read_bytes()
    if not tomllib.loads(credentials.decode()).get("client"):
        raise SystemExit("No users found; refusing an unauthenticated endpoint.")
    # Keep original credential bytes: upstream 1.1.0 has quote-style-sensitive parsing.
    def replace_setting(text, name, value):
        text, count = re.subn(r"(?m)^" + name + r"\s*=.*$", name + " = " + json.dumps(value), text)
        if count != 1:
            raise SystemExit("Cannot unambiguously update " + name)
        return text
    vpn_text = replace_setting(vpn_text, "listen_address", "0.0.0.0:8443")
    vpn_text = replace_setting(vpn_text, "credentials_file", "credentials.toml")
    if vpn.get("rules_file") is not None:
        vpn_text = replace_setting(vpn_text, "rules_file", "rules.toml")
    vpn_text += '\n[metrics]\naddress = "127.0.0.1:1987"\nper_client_metrics = true\n'
    tomllib.loads(vpn_text)
    target.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    staged = Path(tempfile.mkdtemp(prefix=".import-", dir=target.parent))
    try:
        for name in ("hosts.toml", "credentials.toml", "rules.toml"):
            if (source / name).is_file():
                shutil.copyfile(source / name, staged / name)
                (staged / name).chmod(0o600)
        (staged / "vpn.toml").write_text(vpn_text)
        (staged / "vpn.toml").chmod(0o600)
        os.rename(staged, target)
    finally:
        if staged.exists():
            shutil.rmtree(staged)
    print("Imported configuration; original credential bytes and TLS paths preserved.")


if __name__ == "__main__":
    main()
