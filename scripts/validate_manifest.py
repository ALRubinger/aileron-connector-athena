#!/usr/bin/env python3
"""Committed regression guard for connector/manifest.toml invariants.

Stdlib-only (python3 tomllib, 3.11+) per the aileron-family convention of
keeping CI manifest checks free of any Go TOML dependency. Run by the CI
`Validate manifest invariants` step; also runnable locally:

    python3 scripts/validate_manifest.py

Exits non-zero (and prints every violation) if any invariant is broken, so
the change can never regress unnoticed. Asserted invariants:

  * [capabilities.credential].kind    == "aws_sigv4"
  * [capabilities.credential].service == "athena"
  * [capabilities.credential] has NO region and NO access_key_id keys
    (region and access_key_id come from the binding now, not the manifest)
  * [capabilities.network].hosts is non-empty and every entry is
    athena.<region>.amazonaws.com:443 with a plausible region token
  * no OAuth-style keys anywhere (client_secret / scopes / oauth*)
  * no embedded CR or LF inside any string field
"""

from __future__ import annotations

import os
import re
import sys
import tomllib

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
MANIFEST_PATH = os.path.join(REPO_ROOT, "connector", "manifest.toml")

# Substring-matched OAuth-style key names that must never appear in this
# aws_sigv4 connector's manifest (case-insensitive).
OAUTH_KEY_MARKERS = ("client_secret", "scopes", "oauth")

# A plausible AWS region token: lowercase geo prefix, one or more middle
# segments, and a trailing number (e.g. us-east-1, ap-southeast-4).
REGION_RE = re.compile(r"^[a-z]{2,}-[a-z]+(?:-[a-z]+)*-\d+$")

# Credential keys that now live on the binding and must NOT appear under
# [capabilities.credential] in the manifest.
BINDING_ONLY_CREDENTIAL_KEYS = ("region", "access_key_id")


def _iter_strings(value, path):
    """Yield (dotted-path, string) for every string leaf in the manifest."""
    if isinstance(value, str):
        yield path, value
    elif isinstance(value, dict):
        for key, sub in value.items():
            child = f"{path}.{key}" if path else key
            yield from _iter_strings(sub, child)
    elif isinstance(value, (list, tuple)):
        for index, sub in enumerate(value):
            yield from _iter_strings(sub, f"{path}[{index}]")


def _iter_keys(value, path):
    """Yield (dotted-path, key-name) for every mapping key in the manifest."""
    if isinstance(value, dict):
        for key, sub in value.items():
            child = f"{path}.{key}" if path else key
            yield child, key
            yield from _iter_keys(sub, child)
    elif isinstance(value, (list, tuple)):
        for index, sub in enumerate(value):
            yield from _iter_keys(sub, f"{path}[{index}]")


def validate(manifest: dict) -> list[str]:
    errors: list[str] = []

    credential = manifest.get("capabilities", {}).get("credential", {})
    network = manifest.get("capabilities", {}).get("network", {})

    kind = credential.get("kind")
    if kind != "aws_sigv4":
        errors.append(
            f"[capabilities.credential].kind must be 'aws_sigv4', got {kind!r}"
        )

    service = credential.get("service")
    if service != "athena":
        errors.append(
            f"[capabilities.credential].service must be 'athena', got {service!r}"
        )

    # region and access_key_id now live on the binding, set via
    # `aileron binding setup --region/--access-key-id`. They must NOT be
    # pinned in the manifest credential block.
    for key in BINDING_ONLY_CREDENTIAL_KEYS:
        if key in credential:
            errors.append(
                f"[capabilities.credential].{key} must not be set in the "
                f"manifest; it comes from the binding now"
            )

    hosts = network.get("hosts", [])
    if not hosts:
        errors.append("[capabilities.network].hosts is required and empty")
    for host in hosts:
        # Expected shape: athena.<region>.amazonaws.com:443
        if not (host.startswith("athena.") and host.endswith(".amazonaws.com:443")):
            errors.append(
                f"host {host!r} must match athena.<region>.amazonaws.com:443"
            )
            continue
        host_region = host[len("athena."):-len(".amazonaws.com:443")]
        if not REGION_RE.match(host_region):
            errors.append(
                f"host {host!r} has an implausible region segment {host_region!r}"
            )

    # No OAuth-style keys anywhere in the manifest.
    for dotted, key in _iter_keys(manifest, ""):
        lowered = key.lower()
        if any(marker in lowered for marker in OAUTH_KEY_MARKERS):
            errors.append(f"OAuth-style key not allowed: {dotted}")

    # No CR or LF embedded in any string field.
    for dotted, text in _iter_strings(manifest, ""):
        if "\r" in text or "\n" in text:
            errors.append(f"string field {dotted} contains an embedded CR or LF")

    return errors


def main() -> int:
    try:
        with open(MANIFEST_PATH, "rb") as handle:
            manifest = tomllib.load(handle)
    except FileNotFoundError:
        print(f"ERROR: manifest not found at {MANIFEST_PATH}", file=sys.stderr)
        return 2
    except tomllib.TOMLDecodeError as exc:
        print(f"ERROR: {MANIFEST_PATH} is not valid TOML: {exc}", file=sys.stderr)
        return 2

    errors = validate(manifest)
    if errors:
        print(f"Manifest validation FAILED ({len(errors)} violation(s)):", file=sys.stderr)
        for error in errors:
            print(f"  - {error}", file=sys.stderr)
        return 1

    print(f"Manifest validation OK: {MANIFEST_PATH}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
