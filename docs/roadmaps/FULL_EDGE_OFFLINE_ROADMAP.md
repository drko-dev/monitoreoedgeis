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

### Hito H2 — Python Embebido Standalone (macOS arm64) (COMPLETADO — Branch feat/full-edge-embedded-python)

- **Objetivo:** Integrar un intérprete Python 3.11 standalone hermético para darwin-arm64, eliminando cualquier dependencia de Python de sistema o Homebrew.
- **Dependencias:** H1 completado y mergeado (#138).
- **Archivos/Componentes involucrados:**
  - `deploy/installer/fetch-runtime-deps.sh`: script de adquisición reproducible y staging con validación SHA-256 y Mach-O.
  - `internal/installer/embedded_python.go`: gestor de runtime hermético `EmbeddedPythonManager`, sanitizador `IsolateEnv()`, verificador Mach-O `ValidateMachOArch()`, probador de versión/ABI `ProbeInterpreter()` y validador de biblioteca estándar `VerifyStandardLibrary()`.
  - `internal/installer/assets/runtime-manifest.json`: metadatos fijados de CPython 3.11.9 Standalone para `darwin/arm64` (SHA-256 binario `906779570632e91fc62730f51b2b3ef0327d4992d989d6c9a2b6e7118836c170`, 50168 bytes).
  - `internal/installer/embedded_python_test.go`: suite table-driven de 20 escenarios unitarios y de integración + prueba física con intérprete real verificado.
- **Implementación completada:**
  - Adopción de `astral-sh/python-build-standalone` CPython 3.11.9 (release tag `20240415`, PSF-2.0 license).
  - Verificación criptográfica de archivo (`7af7058f...`, 17915401 bytes) y binario (`90677957...`, 50168 bytes).
  - Aislamiento de entorno en subprocess: purga de `PYTHON*`, `VIRTUAL_ENV`, `__PYVENV_LAUNCHER__`, filtrado de Homebrew/Conda de `PATH`, inyección de `PYTHONNOUSERSITE=1` y ejecución con `-I`.
  - Verificación de arquitectura Mach-O 64-bit arm64 (`macho.CpuArm64`).
  - Verificación de carga hermética de la biblioteca estándar dentro de `sys.prefix`.
- **Tests obligatorios (20 escenarios aprobados con -race):**
  1. Python embebido válido (synthetic fixture).
  2. Python ausente (`ErrArtifactMissing` / `PYTHON_NOT_FOUND`).
  3. Hash incorrecto (`ErrArtifactCorrupted` / `PYTHON_HASH_MISMATCH`).
  4. Tamaño incorrecto (`ErrArtifactCorrupted` / `PYTHON_SIZE_MISMATCH`).
  5. Ejecutable sin permisos (`PYTHON_NOT_EXECUTABLE`).
  6. Arquitectura incorrecta (`PYTHON_ARCH_INCOMPATIBLE`).
  7. Versión incorrecta (`PYTHON_VERSION_INCOMPATIBLE`).
  8. ABI incompatible (`PYTHON_ABI_INCOMPATIBLE`).
  9. Distribución incompleta (`PYTHON_STDLIB_INCOMPLETE`).
  10. Sin Python del sistema en PATH.
  11. Homebrew ausente en PATH.
  12. Python host incompatible ignorado.
  13. PYTHONHOME/PYTHONPATH contaminados purgados.
  14. Symlink externo rechazado (`SECURITY_ESCAPING_SYMLINK`).
  15. Ejecución con timeout (`PYTHON_EXEC_TIMEOUT`).
  16. Ejecución de biblioteca estándar (`STDLIB_VERIFIED`).
  17. Preparación con red bloqueada (air-gapped).
  18. Pack reubicado en ruta diferente.
  19. Rutas con espacios.
  20. Validación repetida e idempotente.
  - Test de integración física: ejecución de intérprete CPython 3.11.9 real en Apple Silicon (`TestEmbeddedPython_RealPythonExecution`).
  - Suite adversarial de empaquetado y extracción segura (14 escenarios en `secure_extractor_test.go`): path traversal, rutas absolutas, symlinks que escapan, hardlinks externos/peligrosos, mismatch de hash pre-extracción, cleanup ante extracción interrumpida, rollback ante fallo de swap, preservación de runtime previo válido, staging garantizado en el mismo filesystem, verificación completa de distribución (dylib + stdlib), validación física real, rollback ante fallo post-activación (Case 13) y preservación de backup ante fallo de rollback (Case 14).
  - Suite de seguridad de rollback y máquinas de estado con inyección determinista de fallos (12 escenarios en `deploy/installer/test-rollback-safety.sh`): primera instalación, actualización limpia, fallo previo a activación, fallo al mover runtime anterior, fallo de activación con rollback automático, fallo al restaurar con preservación estricta de backup, fallo en verificación post-activación con rollback, interrupción de señales (SIGINT) entre renames con rollback, interrupción en cleanup sin reentrancia, idempotencia en ejecución repetida, preservación criptográfica exacta bit a bit, y garantía de no eliminación de backups no recuperados tras finalizar el proceso.
- **Criterios de aceptación (DONE):**
  - El intérprete embebido ejecuta y reporta versión 3.11.9 sin intervención del sistema host.
  - Extractor seguro implementado en Go (`cmd/runtime-extractor` y `internal/installer/secure_extractor.go`) reemplaza `tar -xzf` con validación previa de entradas, límites contra tar-bombs y staging en el mismo filesystem con rollback atómico.
  - Máquina de estados explícita (`INIT` -> `PREPARING` -> `STAGED` -> `BACKING_UP` -> `BACKED_UP` -> `ACTIVATING` -> `ACTIVATED` -> `VERIFYING` -> `COMMITTED`) implementada en `deploy/installer/fetch-runtime-deps.sh`.
  - Preservación estricta de backups: el backup previo nunca se elimina hasta que la verificación post-activación tiene éxito; ante fallo de restauración, el backup se conserva intacto y se reporta en logs.
  - Recuperación basada en evidencia física del filesystem: inspección de `[ -d "$backup_old" ]` en `cleanup()` para activar rollback ante interrupción en cualquier micro-ventana de transición (e.g. antes de asignar `STATE=BACKED_UP`).
  - Verificación física inmediata de backup en disco tras el primer rename antes de avanzar hacia la activación.
  - Delimitación explícita de la ventana no atómica del swap de dos renames en POSIX, documentando que fallos catastróficos no capturables (SIGKILL, kernel panic, corte eléctrico) requieren reconciliación de arranque persistente en H6.
  - `runtime-manifest.json` marcado explícitamente como `template` para componentes no construidos (H3–H5).
  - Estado formal `PYTHON_EXECUTABLE` no implica `RUNTIME_READY` o `INFERENCE_READY`.
  - 100% de tests unitarios, de integración y adversariales aprobados bajo `go test -count=1 -race ./internal/installer/...` y `./deploy/installer/test-rollback-safety.sh` (13 escenarios en 12 categorías).
- **Riesgos:** Mitigado aislamiento mediante flags `-I` y filtrado estricto de variables. Mitigado TOCTOU mediante staging en directorio oculto del mismo filesystem y rename atómico con rollback. Documentada la limitación de reemplazo de directorios de dos pasos en POSIX vs puntero único de H6.
- **Bloqueos:** Ninguno.
- **Entregable:** Branch `feat/full-edge-embedded-python` con `EmbeddedPythonManager`, extractor seguro, script de fetch endurecido con máquina de estados, y suite de tests de rollback.

---

### Hito H3 — Dependencias Offline y Wheelhouse (COMPLETADO)

- **Objetivo:** Proveer e integrar un wheelhouse hermético con versiones exactas (pinned) de PyTorch, Torchvision, Ultralytics, Pillow y dependencias transitivas, instalables con `--no-index`.
- **Dependencias:** H2 completado.
- **Archivos/Componentes involucrados:**
  - `internal/installer/wheelhouse.go`
  - `internal/installer/assets/wheelhouse-lock.json`
  - `internal/installer/wheelhouse_test.go`
  - `deploy/installer/fetch-wheelhouse.sh`
- **Implementación realizada:**
  - Lockfile determinista `assets/wheelhouse-lock.json` con 36 wheels binarios exactos (173.9 MB total), hashes SHA-256 reales, tamaños y metadatos de tags PEP 427.
  - Componente Go `WheelhouseManager` con validación exhaustiva de integridad, confinamiento canónico de symlinks, comprobación de tags de plataforma (`cp311`, `abi3`, `universal2`, `arm64`) y rechazo estricto de distribuciones fuente (.tar.gz, .zip).
  - Protocolo de aprovisionamiento 100% offline invocando `pip install --no-index --find-links=<wheelhouse> --only-binary=:all: --disable-pip-version-check --no-cache-dir`.
  - Contención estricta de red: `PIP_CONFIG_FILE=/dev/null`, `PIP_NO_INDEX=1` y proxy blackhole a `127.0.0.1:9` para fail-closed ante cualquier intento de apertura de socket externo.
  - Aislamiento de entorno con `IsolateEnv()` suprimiendo `PYTHONPATH`, `PYTHONHOME`, `VIRTUAL_ENV` y Homebrew.
  - Sonda de verificación de imports (`VerifyImports`) y prueba de aceleración de hardware Apple Silicon Metal Performance Shaders (`torch.backends.mps.is_available()`) con ejecución tensorial real y fallback a CPU.
  - Smoke test del Vision Worker (`backend.py`) validando resolución de dispositivo sin necesidad de cámaras reales ni pesos en git.
- **Tests obligatorios:**
  - Suite de 25 escenarios unitarios y de integración table-driven cubriendo lock válido/inválido, wheels ausentes/corruptos, hash/size mismatch, versiones/ABI incompatibles, wheels de otra arquitectura, dependencias transitivas faltantes, duplicadas, sdist rechazados, aislamiento de pip config, network fail-closed, host/brew ausentes, PYTHONPATH contaminado, idempotencia, fallos de instalación, y preservación del runtime previo.
  - Prueba física E2E en hardware real (Apple Silicon M4) instalando los 36 wheels en ~20s, validando imports y aceleración MPS.
- **Criterios de aceptación (DONE):**
  - Instalación exitosa sin tráfico saliente de red (comprobado vía blackhole proxy).
  - Duración de preparación reducida drásticamente (18-20s en M4).
  - 100% de tests unitarios, de integración y adversariales aprobados bajo `go test -count=1 -race ./internal/installer/...`.
- **Riesgos:** Mitigado tamaño mediante lockfile estricto y descarga exclusiva en packaging. `MODEL_REDISTRIBUTION_APPROVED=false` estrictamente mantenido.
- **Bloqueos:** Ninguno.
- **Entregable:** Branch `feat/full-edge-offline-wheelhouse` con `WheelhouseManager`, lockfile, suite de tests y script de sincronización.

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
