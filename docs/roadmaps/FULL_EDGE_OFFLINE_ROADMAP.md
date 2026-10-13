# Master Roadmap — Full Edge Self-Contained Offline Runtime

## 1. Overview & Strategy

This document establishes the official engineering roadmap to transition GEO CAM Edge from online-dependent provisioning to a 100% self-contained, offline-ready runtime.

- **Primary Target Platform:** macOS Apple Silicon (arm64, M1–M4).
- **Secondary Target Platforms:** Linux arm64 / Linux amd64.
- **Licensing Constraint:** `MODEL_REDISTRIBUTION_APPROVED=false` remains enforced for commercial model weights until explicit authorization. Runtime infrastructure is separated from model binary licensing.

---

## 2. Milestone Breakdown (H0 to H9)

```
[H0] Baseline & Architecture
 │
 ├──▶ [H1] Runtime Pack Spec & Manifest
 │     │
 ├──▶ [H2] Embedded Standalone Python (macOS arm64)
 │     │
 ├──▶ [H3] Offline Wheelhouse & Deterministic Dependencies
 │     │
 ├──▶ [H4] Bundled Worker, Static FFmpeg & Model Infrastructure
 │     │
 ├──▶ [H5] Installer Facade, Service & UX Integration
 │     │
 ├──▶ [H6] Atomic Staging, Self-Healing, Upgrade & Rollback
 │     │
 ├──▶ [H7] CI Multi-Platform & Adversarial Offline Test Suite
 │     │
 └──▶ [H8] Physical Validation on macOS Apple Silicon M4
       │
       └──▶ [H9] Release Readiness & Multi-Platform Expansion (Linux)
```

---

### Hito H0 — Baseline y Arquitectura (COMPLETADO — PR #137)

- **Objetivo:** Auditar todos los componentes existentes, formalizar el diseño técnico del Runtime Pack versionado, evaluar y decidir la estrategia de packaging para macOS arm64, y establecer el roadmap ejecutable.
- **Dependencias:** PRs #263, #264 y #136 mergeados en `main`.
- **Archivos/Componentes involucrados:**
  - `docs/architecture/FULL_EDGE_SELF_CONTAINED.md`
  - `docs/roadmaps/FULL_EDGE_OFFLINE_ROADMAP.md`
  - `docs/PROJECT_STATUS.md`
- **Implementación esperada:** Documentación arquitectónica exhaustiva, contratos de datos para `runtime-manifest.json`, análisis de tradeoffs y definición de criterios de aceptación para H1–H9.
- **Tests obligatorios:** Preflight de consistencia Git, verificación de enlaces y formato de documentación.
- **Criterios de aceptación (DONE):**
  - Componentes existentes auditados (resolver, provisioner, worker, ffmpeg, models).
  - Decisión técnica justificada (Python Standalone + Pinned Wheelhouse Bootstrap para macOS arm64).
  - Contratos de compatibilidad e integridad definidos.
  - Roadmap H0–H9 documentado con criterios verificables.
  - Cero cambios de código funcional en este hito.
  - PR documental creado y mergeado (#137).
- **Riesgos:** Supuestos erróneos sobre la portabilidad de venvs entre hosts de macOS.
- **Bloqueos:** Ninguno.
- **Entregable:** PR de documentación (`docs/full-edge-offline-h0`, PR #137 MERGED en `911a0e8c62a9d7e33059de33501977e5551f9bf6`).

---

### Hito H1 — Runtime Pack Specification y Manifest (COMPLETADO — Branch feat/full-edge-runtime-manifest)

- **Objetivo:** Definir e implementar el modelo de datos formal en Go y JSON para el `runtime-manifest.json`, integrando verificación criptográfica SHA-256 estricta.
- **Dependencias:** H0 aprobado y mergeado (#137).
- **Archivos/Componentes involucrados:**
  - `internal/installer/runtime_manifest.go`
  - `internal/installer/assets/runtime-manifest.json`
  - `internal/installer/runtime_manifest_test.go`
- **Implementación completada:**
  - Modelos Go: `RuntimeManifest`, `PythonSpec`, `WorkerSpec`, `BinarySpec`, `DependencySpec`, `CompatibilitySpec`, `ArtifactsValidationReport`, `ArtifactValidationResult`.
  - Parsers y validadores: `ParseRuntimeManifest`, `LoadRuntimeManifest`, `LoadEmbeddedRuntimeManifest`, `ValidateRuntimeManifest`.
  - Verificación exhaustiva: `ValidateSchema()`, `ValidateCompatibility()`, `ValidateArtifacts()`, `ValidateArtifactsWithProgress()`.
  - Reglas de seguridad: resolución canónica de la raíz y de cada componente (`filepath.EvalSymlinks`), confinamiento físico sin comparación de prefijos de strings (`isPathConfined`), preservación de symlinks internos legítimos, rechazo de symlinks rotos/cíclicos/escapes (tanto hojas como directorios padre), deduplicación y normalización de sintaxis de rutas (`canonicalizeManifestPath`), prevención de operadores de versiones abiertas (`>=, <=, ~, ^, *`) y streaming de SHA-256 (`hashFileSHA256`).
- **Tests obligatorios (suite table-driven y seguridad de symlinks ejecutados con -race):**
  1. Manifiesto válido (synthetic fixture).
  2. JSON corrupto / malformado.
  3. Schema_version desconocido / no soportado.
  4. Campo obligatorio faltante (missing required fields).
  5. Hash SHA-256 inválido (no-hex, longitud incorrecta).
  6. Tamaño incorrecto (spec <= 0 y mismatch físico).
  7. Archivo físico ausente (`ErrArtifactMissing`).
  8. Archivo truncado (`ErrArtifactCorrupted`).
  9. Archivo modificado / hash mismatch (`ErrArtifactCorrupted`).
  10. Arquitectura incompatible (`arm64` vs `amd64`).
  11. ABI / plataforma incompatible (`darwin` vs `linux`).
  12. Versiones malformadas o rangos abiertos (`ultralytics>=8.3`).
  13. Rutas absolutas (`/bin/python`).
  14. Path traversal (`../../etc/passwd`).
  15. Duplicados (binary names, dependency filenames, relative paths).
  16. Seguridad de symlinks:
      - Symlink de hoja que escapa (`TestRuntimeManifest_Case16_LeafSymlinkEscape`).
      - Symlink de directorio padre que escapa (`TestRuntimeManifest_ParentSymlinkEscape`).
      - Symlinks internos legítimos confinados (`TestRuntimeManifest_ValidInternalSymlinks`).
      - Symlinks rotos en hoja y directorio padre (`TestRuntimeManifest_BrokenSymlink`).
      - Ciclos de symlinks en hojas y directorios (`TestRuntimeManifest_CyclicSymlink`).
      - Semántica de raíz como symlink válida/rota/cíclica (`TestRuntimeManifest_SymlinkRoot`).
      - Normalización de rutas duplicadas (`TestRuntimeManifest_DuplicateNormalizedPaths`).
  17. Archivo grande validado mediante streaming.
  18. Manifiesto con entradas en distinto orden.
- **Criterios de aceptación (DONE):**
  - Esquema JSON documentado y embebido en Go.
  - 100% de tests unitarios aprobados (`go test -race ./internal/installer/...`).
- **Riesgos:** Mitigado TOCTOU mediante especificación de staging en 0700 y rename atómico para H6.
- **Bloqueos:** Ninguno.
- **Entregable:** Branch `feat/full-edge-runtime-manifest` con suite de tests y manifiesto base.

---

### Hito H2 — Python Embebido Standalone (macOS arm64)

- **Objetivo:** Integrar un intérprete Python 3.11 standalone hermético para darwin-arm64, eliminando cualquier dependencia de Python de sistema o Homebrew.
- **Dependencias:** H1 completado.
- **Archivos/Componentes involucrados:**
  - `deploy/installer/fetch-runtime-deps.sh` (o script de preparación)
  - `internal/installer/embedded_python.go`
  - `internal/installer/embedded_python_test.go`
- **Implementación esperada:**
  - Detección y extracción de Python Standalone (ej. `python-build-standalone` 3.11.x) en el data dir de la aplicación.
  - Validación de ejecución aislada sin tocar variables de entorno globales (`PYTHONPATH`, `PYTHONHOME`).
  - Verificación de compatibilidad con aceleración Apple Silicon Metal (MPS).
- **Tests obligatorios:**
  - Test en máquina sin `/usr/bin/python3` en el PATH simulado.
  - Test de ejecución de scripts básicos en sandbox temporal.
- **Criterios de aceptación (DONE):**
  - El intérprete embebido ejecuta y reporta versión 3.11.x sin intervención del sistema host.
- **Riesgos:** Dependencias dinámicas de librerías del sistema macOS que difieran entre macOS 12, 13, 14 y 15.
- **Bloqueos:** Ninguno.
- **Entregable:** Módulo de gestión del intérprete embebido probado.

---

### Hito H3 — Dependencias Offline y Wheelhouse

- **Objetivo:** Proveer e integrar un wheelhouse hermético con versiones exactas (pinned) de PyTorch, Torchvision, Ultralytics, Pillow y dependencias transitivas, instalables con `--no-index`.
- **Dependencias:** H2 completado.
- **Archivos/Componentes involucrados:**
  - `internal/installer/wheelhouse.go`
  - `internal/installer/assets/wheelhouse/` (o empaquetado en release bundle)
  - `internal/installer/wheelhouse_test.go`
- **Implementación esperada:**
  - Lista de dependencias bloqueada sin rangos (`torch==2.4.0`, `ultralytics==8.3.1`, etc.).
  - Protocolo de instalación que invoca `pip install --no-index --find-links=<wheelhouse>`.
  - Detección fail-closed si se intenta abrir un socket TCP a PyPI.
- **Tests obligatorios:**
  - Simulación de entorno con red completamente bloqueada (resolución DNS nula).
  - Verificación de que todos los imports críticos (`torch`, `ultralytics`, `PIL`) cargan exitosamente.
- **Criterios de aceptación (DONE):**
  - Instalación exitosa sin tráfico saliente de red.
  - Duración de preparación reducida drásticamente (sin latencias de descarga).
- **Riesgos:** Wheel con dependencias nativas faltantes para macOS ARM64.
- **Bloqueos:** Tamaño del bundle del instalador (requiere compresión eficiente de wheels).
- **Entregable:** Mecanismo de bootstrap de wheelhouse offline funcional y testeado.

---

### Hito H4 — Worker, FFmpeg Bundled y Model Infrastructure

- **Objetivo:** Empaquetar binarios estáticos de FFmpeg/FFprobe para macOS arm64, scripts del Vision Worker versionados y cableado con `models-manifest.json`.
- **Dependencias:** H3 completado.
- **Archivos/Componentes involucrados:**
  - `internal/installer/ffmpeg.go`
  - `deploy/installer/assemble-release.sh`
  - `internal/installer/vision_runtime.go`
- **Implementación esperada:**
  - Inclusión de `ffmpeg` y `ffprobe` estáticos en el bundle de macOS.
  - `DependencyResolver` selecciona prioritariamente el FFmpeg bundled sin consultar Homebrew.
  - Sincronización del Vision Worker (`worker.py`) con el Runtime Pack.
  - Respeto estricto de la compuerta `MODEL_REDISTRIBUTION_APPROVED=false`.
- **Tests obligatorios:**
  - Ejecución de `ffmpeg -version` sobre el binario bundled.
  - Validación de compatibilidad entre Worker y Runtime.
  - Validación de que ningún peso comercial se publique sin autorización.
- **Criterios de aceptación (DONE):**
  - Host sin FFmpeg preinstalado reporta FFmpeg listo inmediatamente desde el bundle.
- **Riesgos:** Tamaño de binarios de FFmpeg.
- **Bloqueos:** Ninguno.
- **Entregable:** Subsistema multimedia y worker completamente integrados offline.

---

### Hito H5 — Integración con Instalador y UX

- **Objetivo:** Actualizar el flujo de "Preparar Full Edge" en el frontend Wails y el backend Go (`service.go`) para usar el Runtime Pack offline de forma transparente.
- **Dependencias:** H4 completado.
- **Archivos/Componentes involucrados:**
  - `internal/installer/service.go`
  - `internal/installer/processing_mode.go`
  - `cmd/geocam-edge-ui/frontend/src/components/`
  - `cmd/geocam-edge-ui/frontend/src/services/api.ts`
- **Implementación esperada:**
  - Detección en UI de "Runtime Offline Disponible".
  - Botón "Preparar Full Edge" ejecuta la extracción y validación offline con reporte de progreso porcentual.
  - Eliminación de mensajes de error de timeout de PyPI.
- **Tests obligatorios:**
  - Unit tests de `PrepareVisionRuntime` offline en Go.
  - Tests DOM de UI en frontend.
- **Criterios de aceptación (DONE):**
  - Un usuario no técnico completa la preparación con 1 clic sin internet.
- **Riesgos:** Bloqueo de la UI durante la descompresión si no se manejan eventos asíncronos en Wails.
- **Bloqueos:** Ninguno.
- **Entregable:** UI y backend sincronizados y fluidos.

---

### Hito H6 — Upgrade, Rollback y Resiliencia

- **Objetivo:** Implementar la resiliencia operativa: staging atómico en `.tmp`, auto-reparación de runtime ante archivos corruptos o truncados, y rollback automático si una actualización se interrumpe.
- **Dependencias:** H5 completado.
- **Archivos/Componentes involucrados:**
  - `internal/installer/vision_runtime.go`
  - `internal/installer/resilience.go`
  - `internal/installer/resilience_test.go`
- **Implementación esperada:**
  - Ciclo de auto-reparación: escaneo de integridad SHA-256 de archivos del runtime; si un archivo está corrupto, re-extracción atómica desde el paquete base.
  - Mecanismo de backup/rollback: si un upgrade de runtime falla la validación de importación, restauración inmediata de la versión previa.
  - Preservación de configuraciones en `edge.env` sin sobrescritura destructiva.
- **Tests obligatorios:**
  - Inyección de archivo truncado / corrupto y verificación de self-heal.
  - Simulación de proceso interrumpido (SIGKILL) durante la instalación y recuperación posterior.
- **Criterios de aceptación (DONE):**
  - La instalación nunca deja un runtime a medio escribir en la ruta activa.
- **Riesgos:** Espacio en disco insuficiente durante la copia de staging.
- **Bloqueos:** Ninguno.
- **Entregable:** Módulo de resiliencia con 100% de cobertura de fallos adversariales.

---

### Hito H7 — CI y Pruebas Adversariales Offline

- **Objetivo:** Configurar pipelines de GitHub Actions y pruebas de integración que garanticen que la suite de preparación corre con la red bloqueada y valida múltiples arquitecturas.
- **Dependencias:** H6 completado.
- **Archivos/Componentes involucrados:**
  - `.github/workflows/ci.yml`
  - `.github/workflows/installer-build.yml`
  - `internal/installer/offline_test.go`
- **Implementación esperada:**
  - Gate de CI que corre `go test` y pruebas de provisioning con red bloqueada (proxy fake que responde 500 o firewall mock).
  - Verificación de que el build de macOS arm64 incluye todos los componentes y supera el launch check.
- **Tests obligatorios:**
  - Test de suite completa con `GEOCAM_OFFLINE_STRICT=1`.
- **Criterios de aceptación (DONE):**
  - CI en verde en todos los jobs requeridos sin solicitudes salientes a PyPI.
- **Riesgos:** Restricciones de tamaño en artefactos de GitHub Actions.
- **Bloqueos:** Ninguno.
- **Entregable:** Pipeline de CI blindado contra regresiones online.

---

### Hito H8 — Validación Física macOS M4

- **Objetivo:** Ejecutar la validación física en el hardware real (Mac con chip Apple Silicon M4) con la interfaz de red apagada (Wi-Fi y Ethernet desconectados).
- **Dependencias:** H7 completado.
- **Archivos/Componentes involucrados:**
  - Equipo físico Apple Silicon M4.
  - Cámaras RTSP locales del laboratorio.
  - Binario compilado del instalador.
- **Implementación esperada:**
  - Desconexión física de red externa.
  - Ejecución de "Preparar Full Edge" desde la UI.
  - Verificación de inferencia local en Metal (MPS) a los FPS esperados.
  - Confirmación de streaming RTSP de las 2 cámaras locales.
- **Tests obligatorios:**
  - Verificación de `/status` en daemon local (`http://localhost:8091/status`).
  - Inspección de CPU/GPU/MPS sin caídas.
- **Criterios de aceptación (DONE):**
  - Full Edge operativo, 0 dependencias de red externa durante y después de la preparación.
- **Riesgos:** Comportamiento específico de macOS Gatekeeper / ad-hoc code signature en máquina local.
- **Bloqueos:** Modelos comerciales requieren licencia; se usan modelos de prueba autorizados/fixtures.
- **Entregable:** Reporte de evidencia física documentado con métricas reales.

---

### Hito H9 — Release Readiness y Expansión Multiplataforma

- **Objetivo:** Preparar el proceso de release oficial para el nuevo instalador self-contained en macOS arm64 y sentar las bases para la extensión a Linux amd64/arm64.
- **Dependencias:** H8 completado.
- **Archivos/Componentes involucrados:**
  - `docs/RELEASING.md`
  - `deploy/installer/assemble-release.sh`
  - `docs/PROJECT_STATUS.md`
- **Implementación esperada:**
  - Verificación de checklist de release.
  - Generación de `SHA256SUMS.txt` con el instalador self-contained.
  - Auditoría de licenciamiento final y cierre del ciclo.
- **Tests obligatorios:**
  - Verificación de sumas de verificación y extracción en máquina limpia.
- **Criterios de aceptación (DONE):**
  - Instalador listo para ser taggeado por el owner cuando se autorice.
- **Riesgos:** Ninguno.
- **Bloqueos:** Autorización explícita del owner requerida antes de crear cualquier tag o release.
- **Entregable:** Artefactos de release listos y validados.

---

## 3. Estado de Ejecución

| Hito | Nombre | Estado | Bloqueo Legal / Técnico |
| :--- | :--- | :--- | :--- |
| **H0** | Baseline y Arquitectura | **EN CURSO (PR Documental)** | Ninguno |
| **H1** | Runtime Pack Spec & Manifest | PENDIENTE | Ninguno |
| **H2** | Python Embebido Standalone (macOS) | PENDIENTE | Ninguno |
| **H3** | Dependencias Offline & Wheelhouse | PENDIENTE | Ninguno |
| **H4** | Worker, FFmpeg & Model Infra | PENDIENTE | `MODEL_REDISTRIBUTION_APPROVED=false` (gated) |
| **H5** | Integración con Instalador y UX | PENDIENTE | Ninguno |
| **H6** | Upgrade, Rollback & Resiliencia | PENDIENTE | Ninguno |
| **H7** | CI & Pruebas Adversariales Offline | PENDIENTE | Ninguno |
| **H8** | Validación Física macOS M4 | PENDIENTE | Requiere hardware local |
| **H9** | Release Readiness & Expansión | PENDIENTE | Requiere autorización del owner |
