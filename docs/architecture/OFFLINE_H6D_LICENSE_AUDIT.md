# H6D — Auditoría preliminar de licencias y procedencia (2026-10-14)

**Estado: NO-GO para redistribución.** Este inventario técnico no constituye asesoramiento legal ni aprobación de artefactos.

## Hallazgos verificables

- runtime-manifest.json declara manifest_type=template; los hashes de Worker y FFmpeg son de ejemplo y no existe pack de release verificado.
- wheelhouse-lock.json declara 36 paquetes con versiones, orígenes y etiquetas de licencia. Las etiquetas del lock **no equivalen** a revisar el METADATA y LICENSE de los wheels reales.
- fetch-runtime-deps.sh declara Python standalone 3.11.9 y SHA256. No se inspeccionó archivo real ni los textos de licencia de dependencias incluidas.
- internal/evidence/clips.go invoca libx264 explícitamente; el perfil exacto de FFmpeg y su configuración de compilación todavía no están certificados.
- No existen permisos comprobados de redistribución para pesos de modelos; MODEL_REDISTRIBUTION_APPROVED=false se mantiene.

## Matriz de decisiones

| Artefacto | Riesgo | Evidencia requerida | Estado |
|---|---|---|---|
| Ultralytics y pesos | AGPL-3.0 o contrato Enterprise según modelo de distribución/uso | Decisión propietario/legal, contrato o plan de cumplimiento AGPL, licencias de pesos y derechos de terceros | BLOQUEADO |
| FFmpeg y FFprobe | LGPL/GPL según compilación; libx264 usado por encoder de clips | Decidir vía GPL-compatible o rediseñar codec; auditar configure flags, fuentes exactas, parches, NOTICE y binarios | BLOQUEADO |
| Python standalone | Licencias de CPython y librerías distribuidas en el archivo | Revisar metadatos y textos de licencias del archivo oficial exacto | PENDIENTE |
| Torch / torchvision | Dependencias nativas y notices asociados | Inspeccionar wheels reales y librerías transitivas, origen y compatibilidad arm64 | PENDIENTE |
| Otros wheels | 36 etiquetas declaradas no verificadas | Inspección real de todos los LICENSE y METADATA, SBOM y legal approval | PENDIENTE |
| Vision Worker | Fuente / imports y distribución | Auditoría de código del Worker y componentes utilizados | PENDIENTE |

## Fuentes primarias externas

- https://www.ultralytics.com/license — Ultralytics AGPL y Enterprise.
- https://ffmpeg.org/legal.html — FFmpeg LGPL, GPL, nonfree y checklist.
- https://github.com/astral-sh/python-build-standalone/blob/main/docs/running.rst — metadatos y textos de licencias en paquetes Python standalone.
- https://www.python.org/psf/about/legal-and-policies/ — licencias Python.

Las fuentes generales no demuestran que un binario futuro sea redistribuible. Un SHA-256 no sustituye una prueba de origen, firma ni permiso legal.

## Reproducción

python3 deploy/installer/runtime-pack-license-audit.py
python3 deploy/installer/test-runtime-pack-license-audit.py

La herramienta deja explícitamente legal_go=false para los 36 wheels y release_eligible=false: no transforma etiquetas de licencia en aprobaciones.

## Para desbloquear

1. Resolver estrategia Ultralytics y permisos de pesos; mantener gate de modelos.
2. Definir licencia/configuración concreta de FFmpeg considerando libx264.
3. Obtener artefactos autorizados, revisar metadatos/licencias reales, recopilar NOTICE/SBOM.
4. Generar manifiesto release desde archivos reales y pin autenticado aparte.
5. Resolver inmutabilidad/TOCTOU y certificar offline antes de READY.

Esta revisión no descargó, distribuyó ni ejecutó artefactos de release.


## H6D-R6 — Wheel archives physically inspected (2026-10-14)

A new **read-only, offline and bounded** script, `deploy/installer/audit-wheelhouse-evidence.py`, has inspected the **actual 36 pinned wheel archives** inside `runtime-lab/packs/lab-2026.10.14-0e8657f-r4/wheelhouse`. It does not download packages, import third-party modules, copy wheels or distribute license texts.

Observed on Mac M4:
- **36/36** wheel files matched the exact pinned SHA-256, name/version and byte size; **0** unexpected .whl files and **0** technical errors.
- **36/36** wheel archives contained readable `.dist-info/METADATA` and at least one file under `.dist-info/licenses/` or a legacy license/copying/notice file. The script records internal paths, SHA-256 and sizes of the notice text, plus observed package license fields; it does not interpret or approve the legal obligations.
- **9/36** have a `License-Expression` in METADATA; **25/36** have a nonempty legacy `License` field. Presence/absence does not authorize redistribution. Examples: Ultralytics 8.3.1 reports a legacy AGPL-3.0 field; Torch 2.4.0 reports BSD-3; Torchvision 0.19.0 reports BSD; OpenCV 4.11.0.86 reports Apache 2.0. These are **distribution metadata observations only**, not a complete legal review.
- Local FFmpeg portable 8.1.3 `-buildconf` recorded `--disable-network`, `--disable-autodetect`, `--disable-everything`, H.264 decoding and static binaries; it did not show `--enable-libx264` or `--enable-gpl`. This only describes the **laboratory binary**, and does not clear source-archive signature validation, potential linker/library obligations, runtime codec usage by other product components, or legal redistribution.
- Detached signature file `runtime-lab/ffmpeg-source/ffmpeg-8.1.3.tar.xz.asc` is present. Its authenticity and binding to a trusted upstream signing identity remain **NOT VERIFIED**; presence of .asc alone is insufficient.
- The reviewed source artifact's repository lock does not authenticate the downloader or build provenance. The audit intentionally reports `signed_origin_verified=false`, `human_legal_review_complete=false`, `redistribution_approved=false` and `release_eligible=false`.

Local reproduction:

```bash
W=/Users/gustavomarcon/Documents/proyectos/monitoreoedgeis-worktrees/offline-h6d
python3 "$W/deploy/installer/audit-wheelhouse-evidence.py"   --repo "$W"   --wheelhouse "$W/runtime-lab/packs/lab-2026.10.14-0e8657f-r4/wheelhouse" --summary
python3 "$W/deploy/installer/test-audit-wheelhouse-evidence.py"
```

All eight archive-adversarial tests passed (corrupt SHA, wrong distribution identity, ZIP path traversal, missing/symlink wheel, unexpected wheel, missing notices and successful synthetic fixture). The result improves technical evidence but **does not change the NO-GO for release**.


## H6D-R7 — FFmpeg source authenticity update (supersedes R6 signature blocker)

The earlier R6 section records **NOT VERIFIED** because it only found the detached signature file. On 2026-10-14, local FFmpeg **8.1.3 source tarball PGP verification is now PASS**: the signer fingerprint matches the value independently published on FFmpeg's official download page, and the upstream detached signature matches the local one byte-for-byte. Verification used GnuPG with an ephemeral isolated keyring. Exact archive SHA-256: `7138d28c96d9d3e3af4ee3d8cad72741f8ffb40da90c1112235dea3ecd3178a3`. See `OFFLINE_H6D_R7_FFMPEG_SOURCE_AUTHENTICITY.md`.

This resolves **only the signed source archive**. Build provenance for the macOS binary, transitive licenses, model rights, independent release pin, H7/H8 certification remain **BLOCKED**. No broad legal or release approval follows.
