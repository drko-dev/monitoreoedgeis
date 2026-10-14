#!/usr/bin/env python3
"""Assemble a local, hard-linked H6D lab pack. This is never a release tool."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import platform
import shutil
import stat
import subprocess
import tempfile
from datetime import datetime, timezone
from pathlib import Path


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def contained(path: Path, root: Path) -> bool:
    try:
        path.relative_to(root)
        return True
    except ValueError:
        return False


def validate_tree(root: Path) -> None:
    root = root.resolve(strict=True)
    if not root.is_dir():
        raise ValueError(f"source is not a directory: {root}")
    for current, dirs, files in os.walk(root, followlinks=False):
        base = Path(current)
        for name in dirs + files:
            item = base / name
            mode = item.lstat().st_mode
            if stat.S_ISLNK(mode):
                raw_target = os.readlink(item)
                if os.path.isabs(raw_target):
                    raise ValueError(f"absolute symlink rejected: {item}")
                resolved = (item.parent / raw_target).resolve(strict=True)
                if not contained(resolved, root):
                    raise ValueError(f"symlink escapes source tree: {item}")
                if name in dirs:
                    dirs.remove(name)
            elif not (stat.S_ISDIR(mode) or stat.S_ISREG(mode)):
                raise ValueError(f"special file rejected: {item}")


def hardlink_tree(source: Path, destination: Path) -> None:
    source = source.resolve(strict=True)
    validate_tree(source)
    destination.mkdir(parents=True, exist_ok=False)
    for current, dirs, files in os.walk(source, followlinks=False):
        src_dir = Path(current)
        rel_dir = src_dir.relative_to(source)
        dst_dir = destination / rel_dir
        for name in list(dirs):
            src = src_dir / name
            dst = dst_dir / name
            if src.is_symlink():
                os.symlink(os.readlink(src), dst)
                dirs.remove(name)
            else:
                dst.mkdir()
        for name in files:
            src = src_dir / name
            dst = dst_dir / name
            if src.is_symlink():
                os.symlink(os.readlink(src), dst)
            else:
                os.link(src, dst, follow_symlinks=False)


def validate_arm64_binary(path: Path) -> None:
    if path.is_symlink() or not path.is_file() or not os.access(path, os.X_OK):
        raise ValueError(f"binary must be a regular executable file: {path}")
    result = subprocess.run(["file", str(path)], capture_output=True, text=True, check=False)
    if result.returncode or "Mach-O 64-bit executable arm64" not in result.stdout:
        raise ValueError(f"binary is not a darwin/arm64 Mach-O executable: {path}: {result.stdout.strip()}")


def run_checked(args: list[str], *, env: dict[str, str]) -> str:
    result = subprocess.run(args, capture_output=True, text=True, env=env, check=False)
    if result.returncode:
        raise RuntimeError(f"command failed ({result.returncode}): {args[0]}: {result.stderr.strip()}")
    return result.stdout + result.stderr


def wheel_records(lock_path: Path, wheelhouse: Path) -> list[dict[str, object]]:
    lock = json.loads(lock_path.read_text())
    records = []
    for item in lock.get("dependencies", []):
        path = wheelhouse / item["filename"]
        if path.is_symlink() or not path.is_file():
            raise ValueError(f"wheel missing or symlinked: {item['filename']}")
        size = path.stat().st_size
        digest = sha256(path)
        if size != item["size_bytes"] or digest != item["sha256"]:
            raise ValueError(f"wheel hash/size mismatch: {item['filename']}")
        records.append({
            "name": item["name"],
            "version": item["version"],
            "path": f"wheelhouse/{path.name}",
            "size_bytes": size,
            "sha256": digest,
            "origin_declared": item.get("origin", ""),
            "license_declared": item.get("license", "UNKNOWN"),
            "license_verified_from_distribution": False,
            "redistribution_approved": False,
        })
    if len(records) != 36:
        raise ValueError(f"expected the locked 36 wheels, found {len(records)}")
    return records


def file_inventory(root: Path) -> list[dict[str, object]]:
    records = []
    for current, dirs, files in os.walk(root, followlinks=False):
        dirs.sort()
        files.sort()
        base = Path(current)
        for name in dirs + files:
            path = base / name
            if path.is_symlink():
                target = path.resolve(strict=True)
                if not contained(target, root.resolve()):
                    raise ValueError(f"pack symlink escapes pack: {path.relative_to(root)}")
                continue
            if path.is_file():
                records.append({"path": path.relative_to(root).as_posix(), "size_bytes": path.stat().st_size, "sha256": sha256(path), "executable": os.access(path, os.X_OK)})
    return records


def fsync_dir(path: Path) -> None:
    fd = os.open(path, os.O_RDONLY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def assemble(repo: Path, lab: Path, pack_id: str, model_paths: list[str]) -> Path:
    repo = repo.resolve(strict=True)
    lab = lab.resolve(strict=True)
    packs = lab / "packs"
    packs.mkdir(exist_ok=True)
    if not contained(packs.resolve(), lab) or not contained(lab, repo):
        raise ValueError("lab and output must remain inside this repository's runtime-lab directory")
    if not pack_id or any(c not in "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-" for c in pack_id):
        raise ValueError("pack id may contain only letters, digits, dot, underscore and hyphen")
    for model_path in model_paths:
        candidate = Path(model_path)
        if not candidate.is_absolute() or candidate.is_symlink() or not candidate.is_file():
            raise ValueError(f"model references must be absolute regular external test files: {model_path}")
        if candidate.name not in {"yolo11s-pose.pt", "yolo11n.pt"}:
            raise ValueError(f"unexpected test-only model reference: {candidate.name}")
        if contained(candidate.resolve(), lab):
            raise ValueError("model references must remain outside the runtime pack")
    output = packs / pack_id
    if output.exists() or output.is_symlink():
        raise FileExistsError(f"refusing to overwrite existing lab pack: {output}")

    python_src = lab / "python"
    wheel_src = lab / "wheelhouse"
    ffmpeg_src = lab / "ffmpeg-portable"
    worker_src = repo / "internal/installer/assets/vision-worker"
    for source in (python_src, wheel_src, ffmpeg_src, worker_src):
        validate_tree(source)
    wheels = wheel_records(repo / "internal/installer/assets/wheelhouse-lock.json", wheel_src)
    validate_arm64_binary(ffmpeg_src / "ffmpeg")
    validate_arm64_binary(ffmpeg_src / "ffprobe")
    validate_arm64_binary(python_src / "bin/python3.11")
    if not (worker_src / "worker.py").is_file() or not (worker_src / "backend.py").is_file():
        raise ValueError("packaged Vision Worker entrypoint/backend is incomplete")

    stage = Path(tempfile.mkdtemp(prefix=f".{pack_id}.assembling-", dir=packs))
    yolo_config = tempfile.TemporaryDirectory(prefix="geocam-h6d-yolo-")
    env = {
        "PATH": "/usr/bin:/bin",
        "PYTHONHOME": "",
        "PYTHONPATH": "",
        "PYTHONNOUSERSITE": "1",
        "PYTHONDONTWRITEBYTECODE": "1",
        "PIP_CONFIG_FILE": "/dev/null",
        "PIP_NO_INDEX": "1",
        "HF_HUB_OFFLINE": "1",
        "TRANSFORMERS_OFFLINE": "1",
        "YOLO_CONFIG_DIR": yolo_config.name,
    }
    try:
        hardlink_tree(python_src, stage / "python")
        hardlink_tree(wheel_src, stage / "wheelhouse")
        hardlink_tree(ffmpeg_src, stage / "ffmpeg")
        hardlink_tree(worker_src, stage / "worker")
        py = stage / "python/bin/python3.11"
        python_version = run_checked([str(py), "--version"], env=env)
        if "3.11.9" not in python_version:
            raise ValueError(f"unexpected standalone Python version: {python_version.strip()}")
        probe = "import platform,sys,torch,torchvision,ultralytics; print(platform.machine(),sys.version.split()[0],torch.__version__,torch.backends.mps.is_available(),torchvision.__version__,ultralytics.__version__)"
        probe_output = run_checked([str(py), "-I", "-c", probe], env=env)
        runtime_probe = next((line.strip() for line in reversed(probe_output.splitlines()) if line.startswith("arm64 3.11.9 ")), "")
        if not runtime_probe.startswith("arm64 3.11.9 "):
            raise ValueError(f"standalone runtime/import probe failed: {runtime_probe}")
        ffmpeg_version = run_checked([str(stage / "ffmpeg/ffmpeg"), "-version"], env=env).splitlines()[0]
        ffprobe_version = run_checked([str(stage / "ffmpeg/ffprobe"), "-version"], env=env).splitlines()[0]
        records = file_inventory(stage)
        ffmpeg_source = lab / "ffmpeg-source/ffmpeg-8.1.3.tar.xz"
        provenance = {
            "python_standalone": {
                "origin_declared": "https://github.com/astral-sh/python-build-standalone",
                "archive_sha256_declared": "7af7058f7c268b4d87ed7e08c2c7844ef8460863b3e679db3afdce8bb1eedfae",
                "archive_available_for_independent_verification": False,
                "local_interpreter_sha256": sha256(stage / "python/bin/python3.11"),
                "license_review_complete": False,
            },
            "ffmpeg": {
                "origin_declared": "https://ffmpeg.org/releases/ffmpeg-8.1.3.tar.xz",
                "source_archive_sha256_observed": sha256(ffmpeg_source) if ffmpeg_source.is_file() else "",
                "source_signature_verified": False,
                "configuration_summary": "static/minimal H.264 decode, file/pipe protocols; no GPL/libx264 per lab build record",
                "redistribution_reviewed": False,
            },
            "vision_worker": {
                "source_path": "internal/installer/assets/vision-worker",
                "worker_sha256": sha256(stage / "worker/worker.py"),
                "backend_sha256": sha256(stage / "worker/backend.py"),
            },
        }
        manifest = {
            "pack_type": "laboratory-only",
            "pack_id": pack_id,
            "created_utc": datetime.now(timezone.utc).isoformat(),
            "source_commit": subprocess.run(["git", "-C", str(repo), "rev-parse", "HEAD"], capture_output=True, text=True, check=True).stdout.strip(),
            "platform": "darwin",
            "architecture": "arm64",
            "release_eligible": False,
            "trusted_pin_present": False,
            "runtime_ready": False,
            "inference_ready": False,
            "model_redistribution_approved": False,
            "models": {"included": False, "external_test_only_references": model_paths, "authorization_claimed": False},
            "runtime_probe": runtime_probe,
            "python_version": python_version.strip(),
            "ffmpeg_version": ffmpeg_version,
            "ffprobe_version": ffprobe_version,
            "provenance": provenance,
            "wheel_count": len(wheels),
            "wheels": wheels,
            "files": records,
            "limitations": ["Hard-linked local lab assembly; not a relocatable release archive.", "Manifest is not authenticated, signed, or a trusted pin.", "External model references are test-only and no weights are included.", "No Wails provisioning, recovery, full airgap or legal approval is asserted."],
        }
        (stage / "lab-manifest.json").write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n")
        fsync_dir(stage)
        os.replace(stage, output)
        fsync_dir(packs)
        return output
    except BaseException:
        shutil.rmtree(stage, ignore_errors=True)
        raise
    finally:
        yolo_config.cleanup()


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--repo", type=Path, default=Path(__file__).resolve().parents[2])
    parser.add_argument("--lab", type=Path, required=True)
    parser.add_argument("--pack-id", required=True)
    parser.add_argument("--model-reference", action="append", default=[])
    args = parser.parse_args()
    output = assemble(args.repo, args.lab, args.pack_id, args.model_reference)
    print(json.dumps({"status": "LAB_PACK_ASSEMBLED_NOT_RELEASE", "path": str(output), "release_eligible": False, "runtime_ready": False, "inference_ready": False, "models_included": False}, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
