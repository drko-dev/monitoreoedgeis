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
