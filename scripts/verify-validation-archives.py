#!/usr/bin/env python3
"""Verify historical evidence bytes without extracting or executing archives."""

import hashlib
import json
from pathlib import Path, PurePosixPath
import sys
import tarfile


def sha256(data):
    return hashlib.sha256(data).hexdigest()


def verify(root):
    archive_dir = root / "docs" / "validation" / "archive"
    manifest = json.loads((archive_dir / "manifest.json").read_text())
    if manifest["formatVersion"] != 1:
        raise ValueError("unsupported archive manifest version")

    names = set()
    archives = set()
    total = 0
    for bundle in manifest["bundles"]:
        name = bundle["name"]
        if name in names or PurePosixPath(name).name != name or name in (".", ".."):
            raise ValueError(f"duplicate or invalid bundle name: {name}")
        names.add(name)
        expected_archive = f"docs/validation/archive/{name}.tar.gz"
        if bundle["archive"] != expected_archive:
            raise ValueError(f"invalid archive path: {bundle['archive']}")
        archive = root / expected_archive
        archives.add(archive.name)
        data = archive.read_bytes()
        if len(data) != bundle["bytes"] or sha256(data) != bundle["sha256"]:
            raise ValueError(f"archive size or hash mismatch: {archive}")
        expected = {member["path"]: member for member in bundle["members"]}
        if len(expected) != len(bundle["members"]):
            raise ValueError(f"duplicate manifest member: {name}")
        seen = set()
        with tarfile.open(archive, "r:gz") as tar:
            for member in tar:
                path = PurePosixPath(member.name)
                prefix = "docs/validation/" if name == "historical-reports" else f"docs/validation/{name}/"
                if (
                    not member.isfile()
                    or path.is_absolute()
                    or ".." in path.parts
                    or not member.name.startswith(prefix)
                    or member.name not in expected
                    or member.name in seen
                ):
                    raise ValueError(f"unexpected or unsafe member: {member.name}")
                record = expected[member.name]
                contents = tar.extractfile(member).read()
                if (
                    len(contents) != record["bytes"]
                    or sha256(contents) != record["sha256"]
                    or member.mode != record["mode"]
                ):
                    raise ValueError(f"member content or mode mismatch: {member.name}")
                seen.add(member.name)
        if seen != set(expected):
            raise ValueError(f"missing archive members: {name}")
        if not (archive_dir / f"{name}.md").is_file():
            raise ValueError(f"missing archive index: {name}")
        total += len(seen)
    if archives != {path.name for path in archive_dir.glob("*.tar.gz")}:
        raise ValueError("archive files and manifest do not match")
    print(f"PASS: {len(names)} archives, {total} original files; all sizes, SHA-256 hashes and file modes match.")


if __name__ == "__main__":
    try:
        verify(Path(__file__).resolve().parents[1])
    except (KeyError, OSError, ValueError, tarfile.TarError) as error:
        print(f"FAIL: {error}", file=sys.stderr)
        sys.exit(1)
