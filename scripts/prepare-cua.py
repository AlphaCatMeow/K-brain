#!/usr/bin/env python3
"""Pin, verify and embed the complete upstream Cua runtime before Go compilation."""
import argparse
import hashlib
import io
import json
import os
from pathlib import Path, PurePosixPath
import stat
import tarfile
import urllib.request
import zipfile

ROOT = Path(__file__).resolve().parents[1]


def safe_name(name):
    path = PurePosixPath(name)
    if path.is_absolute() or ".." in path.parts or "\\" in name or ":" in name:
        raise ValueError("unsafe archive path")
    parts = path.parts[1:]
    if not parts:
        return None
    return str(PurePosixPath(*parts))


def prepare(target, source=None, output=None):
    lock = json.loads((ROOT / "scripts/cua.lock.json").read_text())
    entry = lock["platforms"][target]
    data = Path(source).read_bytes() if source else urllib.request.urlopen(entry["url"], timeout=120).read()
    if hashlib.sha256(data).hexdigest() != entry["sha256"]:
        raise ValueError("Cua archive SHA-256 mismatch")
    files = {}
    if entry["asset"].endswith(".zip"):
        with zipfile.ZipFile(io.BytesIO(data)) as archive:
            for item in archive.infolist():
                name = safe_name(item.filename)
                if not name or item.is_dir():
                    continue
                if stat.S_ISLNK(item.external_attr >> 16):
                    raise ValueError("archive symlink is unsupported")
                if name in files:
                    raise ValueError("duplicate archive entry")
                files[name] = (archive.read(item), 0o755 if name.endswith(".exe") else 0o644)
    else:
        with tarfile.open(fileobj=io.BytesIO(data), mode="r:gz") as archive:
            for item in archive:
                name = safe_name(item.name)
                if not name or item.isdir():
                    continue
                if not item.isfile() or name in files:
                    raise ValueError("unsupported or duplicate archive entry")
                files[name] = (archive.extractfile(item).read(), item.mode & 0o777)
    executable = "cua-driver.exe" if target.startswith("windows-") else "cua-driver"
    if executable not in files or "LICENSE" not in files or "THIRD_PARTY_NOTICES.md" not in files:
        raise ValueError("runtime executable or license notices missing")
    if target.startswith("darwin-") and "CuaDriver.app/Contents/MacOS/cua-driver" not in files:
        raise ValueError("signed macOS app missing")
    manifest = {"version": lock["version"], "target": target, "upstream_sha256": entry["sha256"],
                "files": {name: hashlib.sha256(content).hexdigest() for name, (content, _) in files.items()}}
    dest = Path(output) if output else ROOT / "internal/computer/cua/bundle/runtime.zip"
    dest.parent.mkdir(parents=True, exist_ok=True)
    temporary = dest.with_suffix(".tmp")
    try:
        with zipfile.ZipFile(temporary, "w", compression=zipfile.ZIP_DEFLATED) as archive:
            archive.writestr("bundle.json", json.dumps(manifest))
            for name, (content, mode) in sorted(files.items()):
                item = zipfile.ZipInfo(name)
                item.external_attr = (stat.S_IFREG | mode) << 16
                item.compress_type = zipfile.ZIP_DEFLATED
                archive.writestr(item, content)
        os.replace(temporary, dest)
    finally:
        temporary.unlink(missing_ok=True)
    print(f"Embedded Cua {lock['version']} for {target}: {len(files)} files; SHA-256 verified")


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--target", required=True)
    parser.add_argument("--source")
    parser.add_argument("--output")
    args = parser.parse_args()
    prepare(args.target, args.source, args.output)
