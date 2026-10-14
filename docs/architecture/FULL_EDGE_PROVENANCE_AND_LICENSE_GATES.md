# GEO CAM Edge — Runtime Pack Cryptographic Provenance, SBOM & Licensing Gates

> **Document Status:** Active Specification & Compliance Blueprint  
> **Target Release:** Full Edge Self-Contained Offline Runtime (Hito H3 / H4 / H9)  
> **Primary Target Platform:** macOS Apple Silicon (`darwin/arm64`, M1–M4)  
> **Secondary Target Platforms:** Linux (`linux/arm64`, `linux/amd64`)  
> **Operational Readiness:** `RUNTIME_READY=false`, `INFERENCE_READY=false`, `MODEL_REDISTRIBUTION_APPROVED=false`, `LEGAL_COMMERCIAL_APPROVAL_PENDING=true`  

---

## 1. Executive Summary & Governance Model

GEO CAM Edge is evolving from online, network-dependent provisioning to a 100% self-contained, air-gapped offline runtime pack. In mission-critical edge installations (e.g., enterprise perimeters, transportation hubs, industrial facilities), network access is either heavily restricted or entirely absent. Consequently, the edge appliance cannot rely on PyPI, external package repositories, or online distribution endpoints at deployment time.

With this autonomy comes heightened supply chain risk. An offline runtime pack bundles native executables, Python interpreters, C-extension dynamic libraries, neural network execution graphs, and multimedia transcoders. If any artifact within this pack is modified, substituted, or corrupted—whether by a malicious adversary, a compromised distribution channel, or storage failure—the edge runtime must **fail closed** before executing any untrusted code.

This document establishes the official **Cryptographic Provenance, Software Bill of Materials (SBOM), and Licensing Governance Framework** for GEO CAM Edge.

### Governance Principles

1. **Decoupled Trust Anchors:** The runtime pack cannot certify its own authenticity. Self-signed or internally packaged keys are untrusted by definition. Verification requires an explicit trust anchor (an authoritative Ed25519 or ECDSA P-256 public key) managed independently by the client software or hardware appliance.
2. **Deterministic & Reproducible Artifactory:** Every binary artifact—including CPython 3.11.9, FFmpeg 8.1.3, and the 36 pinned Python wheels—is strictly locked by its exact size in bytes and cryptographic SHA-256 digest. No floating version tags, range operators (`>=`, `~=`), or unverified compilation outputs are permitted.
3. **Multi-Layered Verification Gates:** Integrity verification operates across four distinct barriers:
   - **Gate 1 (Transport & Packaging):** Signed Runtime Manifest verification against authoritative trust anchors.
   - **Gate 2 (Payload Integrity):** Pre-activation bit-for-bit SHA-256 streaming verification of every file in isolated staging.
   - **Gate 3 (Process & Execution Sandbox):** Hermetic execution (`-I`, sanitized environment, no `PYTHONPATH` leaks, local Unix Domain Socket IPC).
   - **Gate 4 (Legal & Licensing Compliance):** Formal copyleft compliance, source disclosure fulfillment, and hard blocking of unapproved model weights (`MODEL_REDISTRIBUTION_APPROVED=false`).

---

## 2. Cryptographic Provenance Architecture

### 2.1 Threat Model & Attack Vectors

The provenance architecture defends against the following concrete attack vectors:

- **Man-in-the-Middle (MitM) & Distribution Tampering:** An attacker intercepts the downloaded or sideloaded runtime pack and replaces a wheel or executable with a trojanized binary.
- **Payload Substitution / Hash Injection:** An attacker modifies both the binary file and the checksum declared in an unsigned manifest. Without an asymmetric digital signature bound to an authoritative trust anchor, checksums provide integrity against accidental corruption, not authenticity against malicious substitution.
- **Signature Replay & Downgrade Attacks:** An attacker presents an outdated, signed runtime pack containing known security vulnerabilities (e.g., older CPython or OpenSSL builds). Mitigated via temporal gating (`issued_at`, `not_before`, `expires_at`).
- **Algorithm Confusion / Key ID Collisions:** An attacker attempts to verify an Ed25519 signature using an ECDSA parser or switches curve parameters. Mitigated via strict algorithm agreement between the trust anchor and signature block, combined with domain-separated signature payloads.
- **TOCTOU (Time-of-Check to Time-of-Use):** An attacker swaps files between manifest validation and process execution. Mitigated via 0700 staging permissions, directory ownership constraints, and atomic directory rename (`os.Rename`).

### 2.2 Signed Runtime Manifest Specification (`SignedRuntimeManifest`)

The trust anchor signs a canonical, domain-separated cryptographic envelope encapsulating the complete `RuntimeManifest`.

#### Schema (`signed-runtime-manifest.json` v1.0.0)

```json
{
  "schema_version": "1.0.0",
  "manifest": {
    "schema_version": "1.0.0",
    "runtime_version": "1.0.0",
    "manifest_type": "release",
    "platform": "darwin",
    "architecture": "arm64",
    "python": {
      "version": "3.11.9",
      "abi": "cp311",
      "relative_binary_path": "python/bin/python3.11",
      "sha256": "906779570632e91fc62730f51b2b3ef0327d4992d989d6c9a2b6e7118836c170",
      "size_bytes": 50168
    },
    "worker": {
      "version": "1.1.0",
      "entrypoint": "worker/worker.py",
      "sha256": "...",
      "size_bytes": 5464
    },
    "binaries": [
      {
        "name": "ffmpeg",
        "relative_path": "ffmpeg/ffmpeg",
        "version": "8.1.3",
        "sha256": "...",
        "size_bytes": 45000000
      },
      {
        "name": "ffprobe",
        "relative_path": "ffmpeg/ffprobe",
        "version": "8.1.3",
        "sha256": "...",
        "size_bytes": 44000000
      }
    ],
    "dependencies": [
      {
        "name": "certifi",
        "version": "2026.7.22",
        "filename": "certifi-2026.7.22-py3-none-any.whl",
        "relative_path": "wheelhouse/certifi-2026.7.22-py3-none-any.whl",
        "sha256": "62f22742b58a1a33014a2b6b706588a8d7e2a88ae7bd1a6ebe8c992928483775",
        "size_bytes": 136983
      }
      /* ... remaining 35 pinned wheels ... */
    ],
    "compatibility": {
      "edge_min_version": "1.0.0",
      "models_pack_min_version": "1.0.0"
    }
  },
  "signature": {
    "key_id": "geocam-edge-release-2026-v1",
    "algorithm": "ed25519",
    "public_key": "O2Y8K3j4qZ7X5M1s6k0r8L...",
    "signature": "R3l4bA8...",
    "issued_at": "2026-10-14T12:00:00.000000000Z",
    "not_before": "2026-10-14T00:00:00.000000000Z",
    "expires_at": "2027-10-14T12:00:00.000000000Z"
  }
}
```

### 2.3 Deterministic Canonicalization & Domain Separation

Digital signatures require absolute bit-level determinism. Any discrepancy in JSON whitespace, key order, or numeric formatting invalidates the signature.

GEO CAM Edge implements canonical serialization through `CanonicalManifestBytes`:
1. The `RuntimeManifest` struct is marshaled to standard JSON.
2. The JSON stream is decoded via `json.NewDecoder` with `.UseNumber()` enabled to prevent precision loss or exponential notation drifting in 64-bit integer values (`size_bytes`).
3. The resulting map structure is recursively marshaled using Go's `encoding/json`, which guarantees that map keys are sorted lexicographically at every hierarchy level.
4. The canonical payload is prefixed with a domain separation header:

```
GEO-CAM-EDGE-SIGNED-MANIFEST-V1\n
alg:<algorithm>\n
key:<key_id>\n
issued:<issued_at RFC3339Nano>\n
expires:<expires_at RFC3339Nano or empty>\n
not_before:<not_before RFC3339Nano or empty>\n
\n
<canonical_manifest_json_bytes>
```

This domain separator guarantees that:
- Signatures cannot be transplanted across different environments or artifact types.
- The signature explicitly commits to the key ID, the cryptographic algorithm, and the temporal validity window.

### 2.4 Cryptographic Algorithms & Trust Store

The Go contract `internal/installer/signed_manifest.go` supports two asymmetric signature suites:

| Suite | Algorithm | Key Representation | Signature Format |
|---|---|---|---|
| **Primary** | `ed25519` (Edwards-curve Digital Signature Algorithm over Curve25519) | 32-byte raw public key | 64-byte Ed25519 signature (Base64) |
| **Secondary** | `ecdsa-p256` (ECDSA with NIST P-256 curve & SHA-256) | ASN.1 PKIX DER public key | ASN.1 DER signature or IEEE P1363 (Base64) |

#### Trust Anchor Decoupling & Revocation

The `TrustStore` interface abstracts the client's authority:
```go
type TrustAnchor struct {
    KeyID            string
    Algorithm        string
    PublicKey        []byte
    Revoked          bool
    RevokedAt        *time.Time
    RevocationReason string
}

type TrustStore interface {
    GetTrustAnchor(keyID string) (*TrustAnchor, error)
}
```

If a signing key is revoked (e.g., due to CI key rotation or build infrastructure decommissioning), the trust anchor registers `Revoked: true`. The validator immediately rejects the manifest with `ErrKeyRevoked`, logging the audit reason.

---

## 3. Consolidated Software Bill of Materials (SBOM)

### 3.1 Primary Runtime & Core Interpreters

| Component | Upstream Source & Version | Target Platform | SPDX License | Size (Bytes) | SHA-256 Checksum | Origin / Build Recipe |
|---|---|---|---|---|---|---|
| **CPython Standalone (Archive)** | Python 3.11.9 (Release `20240415`) | `darwin/arm64` | `PSF-2.0` | 17,915,401 | `7af7058f7c268b4d87ed7e08c2c7844ef8460863b3e679db3afdce8bb1eedfae` | `https://github.com/astral-sh/python-build-standalone/releases/download/20240415/...` |
| **CPython Standalone (Binary)** | `python/bin/python3.11` | `darwin/arm64` (Mach-O) | `PSF-2.0` | 50,168 | `906779570632e91fc62730f51b2b3ef0327d4992d989d6c9a2b6e7118836c170` | Hermetic standalone binary extracted from verified archive |

### 3.2 Multimedia Engine: FFmpeg 8.1.3 (R9 Deterministic Build)

| Component | Version | SPDX License | Compilation Constraints | Upstream PGP Verification | Distribution Model |
|---|---|---|---|---|---|
| **FFmpeg Binary** | `8.1.3` (or `7.1.5` static base) | `LGPL-2.1-or-later` | Strict LGPLv2.1+; `--disable-everything`, `--enable-decoder=h264`, `--extra-ldflags="-static"`; **NO `--enable-gpl`**, **NO `libx264`** | Verified with Michael Niedermayer's PGP release key (`DD11 FA73 12D1 0752 D4C8 B603 A877 39A8 2322 96B8`) | Standalone native executable invoked via pipe IPC (`os/exec`); no CGo linking |
| **FFprobe Binary** | `8.1.3` | `LGPL-2.1-or-later` | Strict LGPLv2.1+ minimal container analysis | Same as FFmpeg | Standalone executable invoked during stream discovery |

### 3.3 The 36 Pinned Python Wheels (Wheelhouse Lockfile)

The following 36 wheels constitute the complete, hermetically closed transitive closure required by PyTorch, Torchvision, Ultralytics, and Pillow on macOS Apple Silicon (`darwin/arm64`) + CPython 3.11. Every file is stored in `internal/installer/assets/wheelhouse-lock.json` and verified with pip `--require-hashes`:

| # | Package Name | Locked Version | Wheel Filename | Size (Bytes) | SHA-256 Checksum | SPDX License | Origin URL |
|---|---|---|---|---|---|---|---|
| 1 | `certifi` | 2026.7.22 | `certifi-2026.7.22-py3-none-any.whl` | 136,983 | `62f22742b58a1a33014a2b6b706588a8d7e2a88ae7bd1a6ebe8c992928483775` | `MPL-2.0` | `https://pypi.org/project/certifi/2026.7.22/` |
| 2 | `charset-normalizer` | 3.5.2 | `charset_normalizer-3.5.2-cp311-cp311-macosx_10_9_universal2.whl` | 370,642 | `3d21b8b13c7592db2ac5e544a6d83187b995257472b0c9e8351b6d507ae37ed6` | `MIT` | `https://pypi.org/project/charset-normalizer/3.5.2/` |
| 3 | `contourpy` | 1.3.3 | `contourpy-1.3.3-cp311-cp311-macosx_11_0_arm64.whl` | 270,149 | `23416f38bfd74d5d28ab8429cc4d63fa67d5068bd711a85edb1c3fb0c3e2f381` | `BSD-3-Clause` | `https://pypi.org/project/contourpy/1.3.3/` |
| 4 | `cycler` | 0.12.1 | `cycler-0.12.1-py3-none-any.whl` | 8,321 | `85cef7cff222d8644161529808465972e51340599459b8ac3ccbac5a854e0d30` | `BSD-3-Clause` | `https://pypi.org/project/cycler/0.12.1/` |
| 5 | `filelock` | 4.1.1 | `filelock-4.1.1-py3-none-any.whl` | 132,460 | `3f4a557945a7b0f95efeb1f432267affe5d45ac8ddde2aed1b97ebb62382c089` | `MIT` | `https://pypi.org/project/filelock/4.1.1/` |
| 6 | `fonttools` | 4.66.1 | `fonttools-4.66.1-cp311-cp311-macosx_10_9_universal2.whl` | 3,089,406 | `d4f76868aea9cc4ce47fdbeaa904c02ee7d85dd0ad095071ae77f0bda6e62cf5` | `MIT` | `https://pypi.org/project/fonttools/4.66.1/` |
| 7 | `fsspec` | 2026.9.0 | `fsspec-2026.9.0-py3-none-any.whl` | 221,738 | `8dd6e646e99ea382bd85f97a45e6b526a442d79423a7dc673f1e2756d05fcb5f` | `BSD-3-Clause` | `https://pypi.org/project/fsspec/2026.9.0/` |
| 8 | `idna` | 3.20 | `idna-3.20-py3-none-any.whl` | 69,583 | `ab7ae7122974553370f0bdb919e1a960b2cd1bc1ef0276416d896db81c14582c` | `BSD-3-Clause` | `https://pypi.org/project/idna/3.20/` |
| 9 | `jinja2` | 3.1.6 | `jinja2-3.1.6-py3-none-any.whl` | 134,899 | `85ece4451f492d0c13c5dd7c13a64681a86afae63a5f347908daf103ce6d2f67` | `BSD-3-Clause` | `https://pypi.org/project/jinja2/3.1.6/` |
| 10 | `kiwisolver` | 1.5.1 | `kiwisolver-1.5.1-cp311-cp311-macosx_11_0_arm64.whl` | 64,071 | `dc1a26b8e53395a01c2c611e58602fa47461f136fba7cd5542e6db6d64be1839` | `BSD-3-Clause` | `https://pypi.org/project/kiwisolver/1.5.1/` |
| 11 | `markupsafe` | 3.0.4 | `markupsafe-3.0.4-cp311-cp311-macosx_11_0_arm64.whl` | 12,063 | `7d3391b2188d18737cb2fa147028b1096236eaa7e156446c650a489fa2cadc91` | `BSD-3-Clause` | `https://pypi.org/project/markupsafe/3.0.4/` |
| 12 | `matplotlib` | 3.11.2 | `matplotlib-3.11.2-cp311-cp311-macosx_11_0_arm64.whl` | 9,290,872 | `c27e577ece613ea12a0790e00b4eb80d901c59a3e16cad474f31d8b1529690b9` | `PSF-compatible` | `https://pypi.org/project/matplotlib/3.11.2/` |
| 13 | `mpmath` | 1.3.0 | `mpmath-1.3.0-py3-none-any.whl` | 536,198 | `a0b2b9fe80bbcd81a6647ff13108738cfb482d481d826cc0e02f5b35e5c88d2c` | `BSD-3-Clause` | `https://pypi.org/project/mpmath/1.3.0/` |
| 14 | `networkx` | 3.6.1 | `networkx-3.6.1-py3-none-any.whl` | 2,068,504 | `d47fbf302e7d9cbbb9e2555a0d267983d2aa476bac30e90dfbe5669bd57f3762` | `BSD-3-Clause` | `https://pypi.org/project/networkx/3.6.1/` |
| 15 | `numpy` | 1.26.4 | `numpy-1.26.4-cp311-cp311-macosx_11_0_arm64.whl` | 13,997,127 | `edd8b5fe47dab091176d21bb6de568acdd906d1887a4584a15a9a96a1dca06ef` | `BSD-3-Clause` | `https://pypi.org/project/numpy/1.26.4/` |
| 16 | `opencv-python` | 4.11.0.86 | `opencv_python-4.11.0.86-cp37-abi3-macosx_13_0_arm64.whl` | 37,326,322 | `432f67c223f1dc2824f5e73cdfcd9db0efc8710647d4e813012195dc9122a52a` | `Apache-2.0` | `https://pypi.org/project/opencv-python/4.11.0.86/` |
| 17 | `packaging` | 26.3 | `packaging-26.3-py3-none-any.whl` | 129,956 | `d7193f7c8e4e93f444fde0262bf90af30e16fa0ad0ad44cb553c87339b23cd1c` | `Apache-2.0 OR BSD-2-Clause` | `https://pypi.org/project/packaging/26.3/` |
| 18 | `pandas` | 3.0.6 | `pandas-3.0.6-cp311-cp311-macosx_11_0_arm64.whl` | 10,023,718 | `d7564d86a94c2eb8ab290b07f63ddaae5c032fa53897c29a2ff2197d43aee8af` | `BSD-3-Clause` | `https://pypi.org/project/pandas/3.0.6/` |
| 19 | `pillow` | 10.4.0 | `pillow-10.4.0-cp311-cp311-macosx_11_0_arm64.whl` | 3,375,655 | `dfe91cb65544a1321e631e696759491ae04a2ea11d36715eca01ce07284738be` | `MIT-CMU` | `https://pypi.org/project/pillow/10.4.0/` |
| 20 | `psutil` | 7.2.2 | `psutil-7.2.2-cp36-abi3-macosx_11_0_arm64.whl` | 129,859 | `1a7b04c10f32cc88ab39cbf606e117fd74721c831c98a27dc04578deb0c16979` | `BSD-3-Clause` | `https://pypi.org/project/psutil/7.2.2/` |
| 21 | `py-cpuinfo` | 9.0.0 | `py_cpuinfo-9.0.0-py3-none-any.whl` | 22,335 | `859625bc251f64e21f077d099d4162689c762b5d6a4c3c97553d56241c9674d5` | `MIT` | `https://pypi.org/project/py-cpuinfo/9.0.0/` |
| 22 | `pyparsing` | 3.3.3 | `pyparsing-3.3.3-py3-none-any.whl` | 126,420 | `ece8c00a69cf01b45d0b1dedabb469c90d8caf996d4fda40f147627a122849a4` | `MIT` | `https://pypi.org/project/pyparsing/3.3.3/` |
| 23 | `python-dateutil` | 2.9.0.post0 | `python_dateutil-2.9.0.post0-py2.py3-none-any.whl` | 229,892 | `a8b2bc7bffae282281c8140a97d3aa9c14da0b136dfe83f850eea9a5f7470427` | `Apache-2.0 OR BSD-3-Clause` | `https://pypi.org/project/python-dateutil/2.9.0.post0/` |
| 24 | `pyyaml` | 6.0.3 | `pyyaml-6.0.3-cp311-cp311-macosx_11_0_arm64.whl` | 175,577 | `652cb6edd41e718550aad172851962662ff2681490a8a711af6a4d288dd96824` | `MIT` | `https://pypi.org/project/pyyaml/6.0.3/` |
| 25 | `requests` | 2.34.2 | `requests-2.34.2-py3-none-any.whl` | 73,075 | `2a0d60c172f83ac6ab31e4554906c0f3b3588d37b5cb939b1c061f4907e278e0` | `Apache-2.0` | `https://pypi.org/project/requests/2.34.2/` |
| 26 | `scipy` | 1.17.1 | `scipy-1.17.1-cp311-cp311-macosx_14_0_arm64.whl` | 20,334,032 | `a3472cfbca0a54177d0faa68f697d8ba4c80bbdc19908c3465556d9f7efce9ee` | `BSD-3-Clause` | `https://pypi.org/project/scipy/1.17.1/` |
| 27 | `seaborn` | 0.13.2 | `seaborn-0.13.2-py3-none-any.whl` | 294,914 | `636f8336facf092165e27924f223d3c62ca560b1f2bb5dff7ab7fad265361987` | `BSD-3-Clause` | `https://pypi.org/project/seaborn/0.13.2/` |
| 28 | `six` | 1.17.0 | `six-1.17.0-py2.py3-none-any.whl` | 11,050 | `4721f391ed90541fddacab5acf947aa0d3dc7d27b2e1e8eda2be8970586c3274` | `MIT` | `https://pypi.org/project/six/1.17.0/` |
| 29 | `sympy` | 1.14.0 | `sympy-1.14.0-py3-none-any.whl` | 6,299,353 | `e091cc3e99d2141a0ba2847328f5479b05d94a6635cb96148ccb3f34671bd8f5` | `BSD-3-Clause` | `https://pypi.org/project/sympy/1.14.0/` |
| 30 | `torch` | 2.4.0 | `torch-2.4.0-cp311-none-macosx_11_0_arm64.whl` | 62,144,370 | `f169b4ea6dc93b3a33319611fcc47dc1406e4dd539844dcbd2dec4c1b96e166d` | `BSD-3-Clause` | `https://pypi.org/project/torch/2.4.0/` |
| 31 | `torchvision` | 0.19.0 | `torchvision-0.19.0-cp311-cp311-macosx_11_0_arm64.whl` | 1,660,227 | `dbf3aa71a3899244fc884303ed3c4604a160824fefac77e82317a5463efc1d9b` | `BSD-3-Clause` | `https://pypi.org/project/torchvision/0.19.0/` |
| 32 | `tqdm` | 4.70.1 | `tqdm-4.70.1-py3-none-any.whl` | 80,199 | `c293e525e6fef9c20e8728fd4612df02a0aa31bb5fe91ecd93e123b1b7bffa73` | `MPL-2.0 AND MIT` | `https://pypi.org/project/tqdm/4.70.1/` |
| 33 | `typing-extensions` | 4.16.0 | `typing_extensions-4.16.0-py3-none-any.whl` | 45,571 | `481caa481374e813c1b176ada14e97f1f67a4539ce9cfeb3f350d78d6370c2e8` | `PSF-2.0` | `https://pypi.org/project/typing-extensions/4.16.0/` |
| 34 | `ultralytics` | 8.3.1 | `ultralytics-8.3.1-py3-none-any.whl` | 881,286 | `dc29d5dc7b6bbbd7d8f20baee54ff7a38a422c5fb2e954ebdf314885eaba3756` | `AGPL-3.0` | `https://pypi.org/project/ultralytics/8.3.1/` |
| 35 | `ultralytics-thop` | 2.2.2 | `ultralytics_thop-2.2.2-py3-none-any.whl` | 32,582 | `10ef0f4ab45261516008660619fef114cddc2b7c8383d9d3bd76c1e551208ca3` | `AGPL-3.0` | `https://pypi.org/project/ultralytics-thop/2.2.2/` |
| 36 | `urllib3` | 2.8.0 | `urllib3-2.8.0-py3-none-any.whl` | 135,717 | `0cf3cae568d36aa9576b28dfb35f11328f1cb974ca7647d9475ebb86c75ac6e3` | `MIT` | `https://pypi.org/project/urllib3/2.8.0/` |

**Total Wheelhouse Size:** `173,935,126` bytes (~165.88 MB).

---

## 4. License Compatibility & Legal Gates Audit

### 4.1 Classification Taxonomy

```
┌────────────────────────────────────────────────────────────────────────┐
│                        GEO CAM EDGE APPLIANCE                          │
│                                                                        │
│  [Go Edge Daemon] ──(Unix Domain Socket / IPC)──▶ [Vision Worker Subproc]│
│   (Proprietary)                                    (CPython 3.11.9)    │
│                                                           │            │
│  [FFmpeg 8.1.3] ◀──(os/exec stdin/stdout pipe)───────────┤            │
│   (LGPL-2.1+)                                             ▼            │
│                                                   [Wheelhouse (36)]    │
│                                                   - Permissive (32)    │
│                                                   - Weak Copyleft (2)  │
│                                                   - AGPL-3.0 (2)       │
└────────────────────────────────────────────────────────────────────────┘
```

The runtime pack dependencies fall into four primary licensing categories:

1. **Permissive (MIT, BSD-2-Clause, BSD-3-Clause, Apache-2.0, PSF-2.0, MIT-CMU):**
   - 32 packages in the wheelhouse, plus CPython Standalone.
   - **Compliance Obligation:** Retain original copyright notices, license texts, and disclaimer of warranties. This is satisfied by embedding the `.dist-info/LICENSE*` files in the distributed wheelhouse and installed `site-packages`.
   - **Patent Grants (Apache 2.0):** `opencv-python`, `requests`, `packaging`, and `python-dateutil` include explicit patent licenses.

2. **Weak / File-Level Copyleft (MPL-2.0):**
   - `certifi` (2026.7.22), `tqdm` (4.70.1).
   - **Compliance Obligation:** Modifications to MPL-covered source files must be made available under MPL-2.0. Clean, unmodified distribution of pre-compiled wheels does not trigger copyleft requirements on adjoining proprietary modules.

3. **Library Copyleft (LGPL-2.1-or-later) — FFmpeg:**
   - **Technical Boundary:** FFmpeg and FFprobe are packaged as standalone static executables, decoupled from the Go daemon and Python worker. They are invoked strictly via `os/exec.Command` communicating over stdin/stdout pipes. There is no static CGo linkage or dynamic memory linking (`dlopen`) with proprietary application code.
   - **Build Recipe Purity:** FFmpeg is compiled with `--disable-everything` and selective parser/decoder activation. It strictly omits `--enable-gpl` and `--enable-nonfree`. Consequently, GPL-only codecs (e.g., `libx264`, `libx265`, `xvid`) are excluded from this build.
   - **Source Distribution Requirement (Section 6, LGPLv2.1):** When distributing the appliance or runtime pack containing the FFmpeg binary, the vendor must provide:
     1. The complete corresponding source code of FFmpeg (version 8.1.3 / 7.1.5).
     2. The exact build script (`deploy/appliance/scripts/build-ffmpeg-static.sh` / `Dockerfile`).
     3. A mechanism allowing the user to rebuild and replace the FFmpeg binary without modifying the proprietary software.

4. **Strong Network Copyleft (AGPL-3.0) — Ultralytics:**
   - `ultralytics` (8.3.1), `ultralytics-thop` (2.2.2).
   - **Technical Isolation:** Executed within the isolated Python Vision Worker child process, communicating over a localhost Unix Domain Socket (`/var/lib/geocam-edge/vision.sock`).
   - **Legal Boundary (Crucial Distinction):** Architectural isolation over a Unix Domain Socket is a technical control, **not** an automatic legal shield under AGPL-3.0 Section 13 (Remote Network Interaction). Commercial distribution without source code disclosure requires an Ultralytics Enterprise License or complete replacement of Ultralytics with a permissive inference runtime (e.g., ONNX Runtime or proprietary PyTorch code).
   - **Model Weights Copyleft:** Ultralytics distributes default YOLO weights (`yolo11n.pt`, `yolo11s-pose.pt`) under AGPL-3.0. Bundling these weights triggers immediate viral copyleft risks.

### 4.2 Legal Disclaimer & Boundary

> [!CAUTION]
> **LEGAL NOTICE:** The technical classifications, socket isolation models, and license mappings in this document constitute architectural and engineering observations. They do **not** constitute formal legal advice, legal opinions, or an attorney-client communication. Formal clearance for commercial distribution must be rendered by intellectual property legal counsel before setting `LEGAL_COMMERCIAL_APPROVAL_PENDING=false`.

### 4.3 Model Redistribution Enforcement Gate

To eliminate copyright and copyleft exposure, the codebase enforces the following programmatic gates:

```bash
MODEL_REDISTRIBUTION_APPROVED=false
LEGAL_COMMERCIAL_APPROVAL_PENDING=true
```

- Unauthenticated release packs containing `.pt`, `.onnx`, or `models/` artifacts are rejected fail-closed during preflight (`internal/installer/runtime_pack_contract.go` and `internal/installer/runtime_pack_provisioner.go`).
- Model weights must be acquired independently by end-users or provisioned through an enterprise-authorized channel under an explicit customer agreement.

---

## 5. Apple Silicon macOS Trust Chain & Notarization

When distributing GEO CAM Edge on macOS Apple Silicon (`darwin/arm64`), Gatekeeper enforces mandatory code signing, Hardened Runtime validation, and Apple Notary Service tickets.

### 5.1 Certificate Hierarchy

```
Apple Root CA
  └── Apple Worldwide Developer Relations Certification Authority
        └── Developer ID Certification Authority
              └── Developer ID Application: <Team Name> (<Team ID>)
```

### 5.2 Signing Architecture (Inside-Out)

On macOS, bundled applications must be signed hierarchically from the innermost dynamic libraries and helper tools to the outermost application bundle:

1. **Embedded Mach-O Dylibs & Python C-Extensions:**
   All `.so` and `.dylib` files in the CPython standard library and the 36 wheels must be code-signed with the Developer ID Application certificate.
   ```bash
   codesign --force --options runtime --timestamp \
     --sign "Developer ID Application: Company Name (TEAM_ID)" \
     "$BUNDLE_DIR/python/lib/libpython3.11.dylib"

   find "$BUNDLE_DIR/python/lib/python3.11/site-packages" -name "*.so" -exec \
     codesign --force --options runtime --timestamp \
       --sign "Developer ID Application: Company Name (TEAM_ID)" {} +
   ```

2. **Standalone Executables:**
   The standalone CPython binary and static FFmpeg tools:
   ```bash
   codesign --force --options runtime --timestamp \
     --entitlements entitlements.plist \
     --sign "Developer ID Application: Company Name (TEAM_ID)" \
     "$BUNDLE_DIR/python/bin/python3.11"

   codesign --force --options runtime --timestamp \
     --sign "Developer ID Application: Company Name (TEAM_ID)" \
     "$BUNDLE_DIR/ffmpeg/ffmpeg"

   codesign --force --options runtime --timestamp \
     --sign "Developer ID Application: Company Name (TEAM_ID)" \
     "$BUNDLE_DIR/ffmpeg/ffprobe"
   ```

3. **Main Application Bundle (`geocam-edge-ui.app`):**
   ```bash
   codesign --force --options runtime --deep --timestamp \
     --entitlements entitlements.plist \
     --sign "Developer ID Application: Company Name (TEAM_ID)" \
     "geocam-edge-ui.app"
   ```

### 5.3 Entitlements Specification (`entitlements.plist`)

Because CPython and PyTorch allocate executable memory for JIT and dynamic module loading, the Hardened Runtime requires targeted entitlements:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <!-- Required by Python interpreter and PyTorch C-extensions -->
    <key>com.apple.security.cs.allow-jit</key>
    <true/>
    <key>com.apple.security.cs.allow-unsigned-executable-memory</key>
    <true/>
    <!-- Required to dynamically load unbundled C-extensions in site-packages -->
    <key>com.apple.security.cs.disable-library-validation</key>
    <true/>
    <!-- Local camera and peripheral access -->
    <key>com.apple.security.device.camera</key>
    <true/>
    <!-- Local loopback and network socket IPC -->
    <key>com.apple.security.network.client</key>
    <true/>
    <key>com.apple.security.network.server</key>
    <true/>
</dict>
</plist>
```

### 5.4 Notarization & Air-Gapped Ticket Stapling

1. **Archive Generation:**
   ```bash
   ditto -c -k --keepParent "geocam-edge-ui.app" "geocam-edge-installer-macos-arm64.zip"
   ```

2. **Submission via `notarytool`:**
   ```bash
   xcrun notarytool submit "geocam-edge-installer-macos-arm64.zip" \
     --keychain-profile "AC_NOTARY_PROFILE" \
     --wait
   ```

3. **Stapling the Ticket for Offline Execution:**
   The notarization ticket issued by Apple must be stapled directly into the app bundle:
   ```bash
   xcrun stapler staple "geocam-edge-ui.app"
   ```
   **Offline Resilience:** When a ticket is stapled, macOS Gatekeeper validates the bundle's integrity and notarization status locally from the stapled ticket without requiring an active internet connection to Apple's CloudKit servers.

4. **Local Gatekeeper Verification:**
   ```bash
   spctl --assess --type execute --verbose "geocam-edge-ui.app"
   ```

---

## 6. Verification Status & Security Release Gates

| Gate ID | Description | Enforcing Component | Current Status | Release Impact |
|---|---|---|---|---|
| **GATE-01** | **Cryptographic Signed Manifest** | `internal/installer/signed_manifest.go` | **IMPLEMENTED & VERIFIED** (14/14 automated tests passing) | Ed25519 and ECDSA P-256 trust anchors decoupled; fails closed on tampering, expiration, or key revocation. |
| **GATE-02** | **Wheelhouse SHA-256 Lockfile** | `internal/installer/wheelhouse.go` | **IMPLEMENTED & VERIFIED** (27/27 automated tests passing) | Exactly 36 wheels pinned with `--hash=sha256:`. Pip operates in `--require-hashes --no-index` mode. |
| **GATE-03** | **CPython Standalone Integrity** | `internal/installer/embedded_python.go` | **IMPLEMENTED & VERIFIED** (20/20 automated tests passing) | Pinned binary hash (`90677957...`), Mach-O ARM64 verification, stdlib hermetic check. |
| **GATE-04** | **FFmpeg LGPL Purity & Pipeline** | `internal/installer/ffmpeg.go`, `Dockerfile` | **IMPLEMENTED & VERIFIED** | Static build strictly under LGPLv2.1+; zero GPL codecs (`no libx264`). Pipe IPC isolation. |
| **GATE-05** | **Model Redistribution Gate** | `internal/installer/runtime_pack_contract.go` | **BLOCKED (ENFORCED)** (`MODEL_REDISTRIBUTION_APPROVED=false`) | No YOLO weights or uncertified model files are distributed. |
| **GATE-06** | **Commercial Legal Review** | Legal Counsel Review | **PENDING** (`LEGAL_COMMERCIAL_APPROVAL_PENDING=true`) | Requires formal clearance of AGPL-3.0 (Ultralytics) for commercial deployment. |
| **GATE-07** | **macOS Notarization & Stapling** | Release CI (`assemble-release.sh`) | **DOCUMENTED / PENDING SECRETS** | Pipeline specified; awaiting production Developer ID credentials and notary profile. |

---

## 7. Conclusion & Next Steps

With the implementation of the `SignedRuntimeManifest` contract, the complete 36-wheel SBOM catalog, and the formalization of the FFmpeg and AGPL-3.0 licensing boundaries, GEO CAM Edge achieves an enterprise-grade cryptographic supply chain architecture.

Before advancing to general availability (GA) in Hito H9:
1. Legal counsel must formally review the AGPL-3.0 copyleft boundary and decide between an Ultralytics Commercial Enterprise License or an ONNX-based runtime migration.
2. Production Developer ID signing keys must be enrolled in the release CI environment to enable automated signing and `notarytool` ticket stapling.
3. Production runtime releases must be signed by the offline release key corresponding to the pinned client trust anchor.
