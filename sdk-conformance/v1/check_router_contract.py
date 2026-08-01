#!/usr/bin/env python3
"""Self-contained live Router contract guard for public native SDK mirrors."""

from __future__ import annotations

import hashlib
import json
import re
import sys
import urllib.request
from datetime import UTC, datetime
from pathlib import Path
from typing import Any

HTTP_METHODS = ("get", "post", "put", "patch", "delete")
MAX_OPENAPI_BYTES = 4 * 1024 * 1024
SOURCE_SUFFIXES = {".cs", ".go", ".java", ".kt", ".php", ".rb", ".swift"}
QUOTED_VALUE = re.compile(r'''["'](?P<value>[^"'\r\n]+)["']''')
SKIP_PARTS = {".git", ".gradle", ".swiftpm", "bin", "build", "obj", "vendor"}


def canonical_sha256(value: Any) -> str:
    encoded = json.dumps(
        value,
        separators=(",", ":"),
        sort_keys=True,
        ensure_ascii=False,
    ).encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()


def operation_set(document: dict[str, Any]) -> set[tuple[str, str]]:
    return {
        (method.upper(), path)
        for path, item in document.get("paths", {}).items()
        for method in HTTP_METHODS
        if method in item
    }


def manifest_operation_set(manifest: dict[str, Any]) -> set[tuple[str, str]]:
    return {(item["method"], item["path"]) for item in manifest["operations"]}


def accepted_live_digests(
    manifest: dict[str, Any], *, now: datetime | None = None
) -> set[str]:
    accepted = {manifest["canonical_sha256"]}
    transition = manifest.get("canonical_transition")
    if transition is None:
        return accepted
    expires_at = datetime.fromisoformat(transition["expires_at"].replace("Z", "+00:00"))
    current = now or datetime.now(UTC)
    if current < expires_at:
        accepted.update(transition["allowed_sha256"])
    return accepted


def normalized_route(value: str, roots: set[str]) -> str | None:
    value = value.split("?", 1)[0]
    if value.startswith("https://api.octoryn.dev"):
        value = value.removeprefix("https://api.octoryn.dev")
    if value.rstrip("/") in {"/v1", "/api/v1"}:
        return None
    if value.startswith(("/api/v1/", "/v1/")):
        return value
    stripped = value.lstrip("/")
    if value.startswith("/"):
        return f"/v1/{stripped}" if stripped else None
    if "/" not in stripped or stripped.split("/", 1)[0] not in roots:
        return None
    return f"/v1/{stripped}"


def source_route_errors(manifest: dict[str, Any], root: Path) -> list[str]:
    allowed = {path for _, path in manifest_operation_set(manifest)}
    roots = {
        path.removeprefix("/api/v1/").removeprefix("/v1/").split("/", 1)[0]
        for path in allowed
        if path.startswith(("/api/v1/", "/v1/"))
    }
    roots.update(
        item.removeprefix("/v1/").removesuffix("*").split("/", 1)[0]
        for item in manifest["classifications"]["legacy-retire"]
    )
    errors: list[str] = []
    for path in root.rglob("*"):
        relative = path.relative_to(root)
        if (
            not path.is_file()
            or path.suffix not in SOURCE_SUFFIXES
            or path.name.endswith(("Test.java", "Test.kt", "_test.go"))
            or SKIP_PARTS.intersection(relative.parts)
            or {"test", "tests"}.intersection(relative.parts)
        ):
            continue
        text = path.read_text(encoding="utf-8")
        for match in QUOTED_VALUE.finditer(text):
            route = normalized_route(match.group("value"), roots)
            if route is None:
                continue
            interpolation = min(
                (
                    index
                    for token in ("{", "${", "\\(")
                    if (index := route.find(token)) >= 0
                ),
                default=-1,
            )
            valid = route in allowed
            if interpolation >= 0:
                prefix = route[:interpolation]
                relative_prefix = prefix.removeprefix("/v1/").removeprefix("/api/v1/")
                valid = bool(relative_prefix) and any(
                    candidate.startswith(prefix) for candidate in allowed
                )
            if not valid:
                errors.append(f"{relative}: route absent from Router contract: {route}")
    return sorted(set(errors))


def fetch_live(url: str) -> dict[str, Any]:
    request = urllib.request.Request(url, headers={"Accept": "application/json"})
    with urllib.request.urlopen(request, timeout=15) as response:  # noqa: S310
        if response.status != 200 or "json" not in response.headers.get_content_type():
            raise RuntimeError("Router OpenAPI response is unavailable or not JSON")
        payload = response.read(MAX_OPENAPI_BYTES + 1)
    if len(payload) > MAX_OPENAPI_BYTES:
        raise RuntimeError("Router OpenAPI response exceeded 4 MiB")
    return json.loads(payload)


def validate(manifest: dict[str, Any], live: dict[str, Any], root: Path) -> list[str]:
    errors: list[str] = []
    expected_operations = manifest_operation_set(manifest)
    if operation_set(live) != expected_operations:
        errors.append("live Router operation set differs from pinned contract")
    if canonical_sha256(live) not in accepted_live_digests(manifest):
        errors.append("live Router digest differs from pinned contract")
    errors.extend(source_route_errors(manifest, root))
    return errors


def main() -> int:
    root = Path(__file__).resolve().parents[2]
    manifest_path = Path(__file__).with_name("router-api-v1.json")
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    errors = validate(manifest, fetch_live(manifest["source"]), root)
    if errors:
        for error in errors:
            print(f"ERROR: {error}", file=sys.stderr)
        return 1
    print(f"Router contract OK: {manifest['canonical_sha256']}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
