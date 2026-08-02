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
EXPECTED_PATH_COUNT = 52
EXPECTED_OPERATION_COUNT = 56
EXPECTED_CANONICAL_SHA256 = (
    "c35e283581b42dce146407122d5624995224ae487088fc266dd19f53f0be4aec"
)
EXPECTED_OPERATIONS_SHA256 = (
    "894b665e90cdc50ca0eef7cb08c6aed2f4bee4314808008976c3d228da3e6b1e"
)
EXPECTED_CLASSIFICATIONS_SHA256 = (
    "775ed6252bd3520901f23d236be41de69a9bb963005e55df9b3b6c1a868767f2"
)


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
    if value.startswith("api/v1/"):
        return f"/{value}"
    if value.startswith("v1/"):
        return f"/{value}"
    stripped = value.lstrip("/")
    if value.startswith("/"):
        return f"/v1/{stripped}" if stripped else None
    if "/" not in stripped or stripped.split("/", 1)[0] not in roots:
        return None
    return f"/v1/{stripped}"


def looks_like_route_context(text: str, start: int) -> bool:
    prefix = text[max(0, start - 100) : start]
    return re.search(
        r"(?:\bpath\s*[:=]|\b(?:request|get|post|put|patch|delete|fetch)\s*\()\s*$",
        prefix,
        re.IGNORECASE,
    ) is not None


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
            if (
                route is None
                and "/" in match.group("value")
                and looks_like_route_context(text, match.start())
            ):
                route = f"/v1/{match.group('value').lstrip('/')}"
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
    if manifest.get("path_count") != EXPECTED_PATH_COUNT:
        errors.append("pinned Router path count differs from trusted lock")
    if manifest.get("operation_count") != EXPECTED_OPERATION_COUNT:
        errors.append("pinned Router operation count differs from trusted lock")
    if manifest.get("canonical_sha256") != EXPECTED_CANONICAL_SHA256:
        errors.append("pinned Router digest differs from trusted lock")
    if manifest.get("operations_sha256") != EXPECTED_OPERATIONS_SHA256:
        errors.append("pinned Router operation digest differs from trusted lock")
    if canonical_sha256(
        sorted(manifest["operations"], key=lambda item: (item["method"], item["path"]))
    ) != EXPECTED_OPERATIONS_SHA256:
        errors.append("pinned Router operations do not match trusted lock")
    if canonical_sha256(manifest.get("classifications")) != EXPECTED_CLASSIFICATIONS_SHA256:
        errors.append("pinned Router classifications differ from trusted lock")
    if "canonical_transition" in manifest:
        errors.append("expired Router contract transition remains pinned")
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
