# H5/H6 — Reporte de Integración Wails <-> Aprovisionador Transaccional

**Estado:** IMPLEMENTED / TESTED LOCAL (suite sintética de integración Wails <-> Provisioner).  
**Banderas de seguridad:** `RUNTIME_READY=false`, `INFERENCE_READY=false`, `MODEL_REDISTRIBUTION_APPROVED=false`.  
**Rama:** `feat/full-edge-h5-h6-wails-integration`.

---

## 1. Arquitectura del Puente (Wails <-> Aprovisionador)

La integración desacopla estrictamente la interfaz de escritorio Wails del sistema transaccional de archivos subyacente mediante cuatro capas estancas:

```
[ Frontend Wails (JS/TS UI) ]
             |
             v (IPC JSON)
[ Capa 1: Wails Trust Boundary ]
  - cmd/geocam-edge-ui/app.go (App facade)
  - internal/installer/wails_bridge.go (NewWailsSafeFullEdgePreparationAPI)
  - Sanitización de snapshots: ValidateAndSanitizeWailsSnapshot()
             |
             v (FullEdgePreparationAPI)
[ Capa 2: Adaptador de Preparación & Recuperación (H5B / H5D) ]
  - internal/installer/runtime_pack_preparation_adapter.go
  - Máquina de estados en memoria + RestoreFromTransaction()
  - Idempotencia por RequestID y mapeo de errores transaccionales
             |
             v (RuntimePackPreparationBackend)
[ Capa 3: Aprovisionador Transaccional de Runtime Pack (H6B) ]
  - internal/installer/runtime_pack_provisioner.go (TransactionalRuntimePackProvisioner)
  - Preflight estricto (PIN SHA-256 externo, manifiesto, hashes, no-symlinks, modelo bloqueado)
             |
             v (Transacciones ACID en disco)
[ Capa 4: Gestor de Recuperación y Almacenamiento Persistente (H6A) ]
  - internal/installer/recovery_manager.go (RecoveryManager)
  - Journaling persistente (.journal), bloqueo inter-proceso (.lock), staging (.staging.*), backup (.backup.*)
```

### Invariantes Fundamentales de Seguridad (Fail-Closed)
1. **Separación entre Integridad Transaccional y Certificación Funcional:**  
   Que un paquete esté correctamente copiado y verificado en disco (`COMMITTED_UNCERTIFIED`) **NUNCA** se reporta a la UI como `READY` ni `RUNTIME_READY=true`. La UI lo recibe como `FAILED` con código `RUNTIME_UNCERTIFIED` hasta que la suite H7 certifique inferencia funcional.
2. **Sanitización de Frontera Wails:**  
   Cualquier snapshot con esquema inválido se intercepta y reemplaza por `FAILED / INVALID_PREPARATION_STATUS`. Cualquier intento (accidental o malicioso) de emitir `READY` acompañado de un error se convierte en `FAILED / PREPARATION_BACKEND_ERROR`. El error Go subyacente siempre se preserva.
3. **Producción Fail-Closed por Defecto:**  
   `NewApp()` shipping sigue cableado a `UnavailableFullEdgePreparation` (`PREPARATION_NOT_INTEGRATED`). La activación transaccional requiere invocación explícita mediante `NewAppWithPinnedRuntimePack` con un PIN SHA-256 externo confiable.

---

## 2. Suite de Pruebas de Integración Sintética

Implementada en `internal/installer/wails_full_edge_integration_test.go`:

| Test | Escenario Cubierto | Resultado |
|---|---|---|
| `TestWailsFullEdgeIntegration_FullFlow` | Flujo E2E completo: UI -> Bridge -> Preflight -> H6A Transacción -> Almacenamiento persistente en disco -> Idempotencia de RequestID -> Reconciliación en arranque tras reinicio -> Recuperación explícita. | **PASS** |
| `TestWailsFullEdgeIntegration_Cancellation` | Cancelación en progreso: rechazo de cancelación arbitraria en caliente (fail-closed) y manejo seguro de cancelación vía `context.Context` (`StateCancelled` / `PREPARATION_CANCELLED` / `CanStart=true`). | **PASS** |
| `TestWailsFullEdgeIntegration_AbruptActivationCrashAndRecovery` | Simulación de corte abrupto de energía / crash durante el rename de activación (`FaultInjector.AfterRename`), verificación de journal en `ACTIVATING`, recuperación y rollback del estado previo en arranque (`ROLLED_BACK_UNCERTIFIED` / `RECOVERY_REQUIRED`), y reintento limpio posterior a v1.0.1. | **PASS** |
| `TestWailsFullEdgeIntegration_CorruptedManifestAndArtifacts` | Preflight adversarial: rechazo de PIN mismatch (`MANIFEST_PIN_MISMATCH`), JSON corrupto (`MANIFEST_INVALID_JSON`), binario faltante (`ARTIFACT_MISSING`), binario alterado (`ARTIFACT_CORRUPTED`), modelo no autorizado (`MODEL_GOVERNANCE_BLOCKED`), y symlink externo. | **PASS** |
| `TestWailsFullEdgeIntegration_StrictNoFalseReady` | Resistencia adversarial: backend falso que intenta devolver `StateReady` o certificación completa con error; verificación de que el puente Wails nunca filtra `READY` ni `Certification`. | **PASS** |
| `TestWailsFullEdgeIntegration_IdempotentRecoveryReboot` | Prueba de estrés con 5 ciclos de reinicio consecutivos con `RestoreFromTransaction`, `Status` y `Recover`, asegurando idoneidad determinística sin corrupción en disco. | **PASS** |
| `TestWailsFullEdgeIntegration_ProductionDefaultRemainsFailClosed` | Verificación de que el adaptador por defecto de producción (`UnavailableFullEdgePreparation`) no permite iniciar preparación y responde `PREPARATION_NOT_INTEGRATED`. | **PASS** |
| `TestWailsFullEdgeIntegration_ConcurrentBridgeAccess` | Concurrencia de 10 goroutines sobre `Status`, `Start`, `Diagnostic`, `Recover` simultáneos verificando 0 data races y exclusión limpia vía bloqueo H6A. | **PASS** |

---

## 3. Verificación de Calidad

- **Formato:** `gofmt -w` ejecutado sin cambios pendientes.
- **Análisis Estático:** `go vet ./internal/installer/... ./cmd/geocam-edge-ui/...` — 0 advertencias, 0 errores.
- **Race Detector:** `go test -race ./internal/installer/... ./cmd/geocam-edge-ui/...` — **100% PASS**, **0 data races** (duración total ~60s).

---

## 4. Próximos Pasos para la Activación Real

Para promover este subsistema a producción real se requieren los siguientes hitos:
1. **Proveedor Autenticado de Releases y PIN (#160):** Mecanismo de compilación y firma criptográfica que entregue el paquete sellado junto al PIN SHA-256 verificado fuera del paquete.
2. **Revisión Legal y Empaquetado Hermético de FFmpeg / FFprobe:** Validación de licencias LGPL/GPL de binarios estáticos herméticos antes de habilitar su distribución.
3. **Integración con H4B (Lanzador Hermético de Vision Worker):** Conectar el worker Python empaquetado sin dependencias del sistema operativo host.
4. **Gobernanza de Modelos:** Especificación de manifiesto firmado de modelos autorizados (`ModelRedistributionApproved = true` únicamente con licenciamiento verificado).
5. **Certificación H7 Offline:** Ejecución de la prueba de humo de inferencia end-to-end con RTSP loopback.
6. **Validación H8 en Hardware Real:** Pruebas en macOS Apple Silicon / Linux x86_64 físicos sobre máquinas limpias.
