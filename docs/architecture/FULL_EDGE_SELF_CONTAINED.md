# Full Edge Self-Contained Runtime & Model Architecture

## 1. Executive Summary

Full Edge allows GEO CAM appliances to perform local AI video inference (object detection, pose estimation, ANPR) on edge hardware (macOS Apple Silicon, Linux amd64, Linux arm64) without continuous reliance on cloud inference or unstable internet downloads during installation.

**Goal:** Completely eliminate non-deterministic PyPI dependency installation and manual model downloading during setup. Ensure automatic self-healing when model files become corrupt on disk.

---

## 2. Models Manifest Contract (`models-manifest.json`)

All models distributed with or managed by GEO CAM Edge are pinned in `models-manifest.json`.

```json
{
  "pack_version": "1.0.0",
  "worker_compatibility": ">=1.0.0",
  "runtime_compatibility": {
    "python": ">=3.9,<3.13",
    "torch": ">=2.0.0",
    "ultralytics": ">=8.0.0"
  },
  "models": [
    {
      "name": "yolo11s-pose.pt",
      "filename": "yolo11s-pose.pt",
      "version": "11s-pose",
      "purpose": "pose_estimation",
      "min_size_bytes": 10000000,
      "expected_size_bytes": 19688560,
      "sha256": "43950efd914d79ba4a2ff40994f79496aa260a9f5d378ee091c667ce19d45863"
    },
    {
      "name": "yolo11n.pt",
      "filename": "yolo11n.pt",
      "version": "11n",
      "purpose": "object_detection",
      "min_size_bytes": 3000000,
      "expected_size_bytes": 5613764,
      "sha256": "099a07a164b3017a4733568c4d3ac3484f23bdfc5ab200df12403b90a6e60b13"
    }
  ]
}
```

---

## 3. Model Integrity Verification & Self-Healing

### Health States
- **`VALID`**: Regular file exists, `size_bytes >= min_size_bytes`, SHA-256 matches manifest, and PyTorch checkpoint loads without unpickling errors.
- **`MISSING`**: File does not exist at destination path.
- **`CORRUPTED`**: File exists but is smaller than `min_size_bytes` (e.g. 20-byte stub), SHA-256 hash does not match, or unpickling fails (`UnpicklingError: pickle data was truncated`).
- **`INCOMPATIBLE`**: Checkpoint loads but weights architecture is incompatible with current worker.

### Atomic Self-Heal Protocol
When a local model in `$GEOCAM_DATA_DIR/models/` is `CORRUPTED` or `MISSING`:
```
Bundled Model (/assets or app resources)
       │
       ▼
1. Validate bundled source SHA-256 & integrity
       │
       ▼
2. Stream to temporary file: $GEOCAM_DATA_DIR/models/.tmp_<name>_<pid>
       │
       ▼
3. Fsync to disk
       │
       ▼
4. Verify temporary file SHA-256 matches manifest
       │
       ▼
5. Atomic rename: os.Rename(tempPath, targetPath)
```
If anything fails during the copy, the temporary file is deleted, preserving the previous state or surfacing a clean diagnostic.

---

## 4. Offline Runtime Packaging

### Packaging Layers
1. **Host Python Detection / Embedded Runtime**:
   - macOS: System Python 3.9+ or Homebrew Python.
   - Linux: System Python 3.10+ / dedicated isolated environment.
2. **Pre-cached Wheels (Wheelhouse)**:
   - For offline deployment packages, pre-download and verify wheels for the target architecture into `assets/wheelhouse/`.
   - `pip install --no-index --find-links=assets/wheelhouse -r requirements.txt`.
3. **Deterministic Requirements Lock**:
   - Pinned dependencies with exact hashes prevent unexpected upstream breakage.

---

## 5. Commercial Release & Licensing Gate

Model weights (e.g., Ultralytics YOLO models) are subject to specific licensing conditions (AGPL-3.0 vs commercial redistribution licenses).

### Gate Contract
```bash
MODEL_REDISTRIBUTION_APPROVED=false # Default in CI/Release
```
- If `MODEL_REDISTRIBUTION_APPROVED=false`:
  - Release packaging scripts bundle code, runtime scripts, and `models-manifest.json`.
  - Binary weights (`.pt`) are **not** bundled into the public GitHub release zip.
  - The installer provides clear UI instructions or guided import for weights.
- If `MODEL_REDISTRIBUTION_APPROVED=true`:
  - Full binary packaging bundles vetted weights and generates self-contained distributions.
- Ensures zero legal non-compliance while keeping all technical self-healing infrastructure production-ready.
