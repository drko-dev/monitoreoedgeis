# Hito O — Configuración Remota: Edge Runtime Apply

Este documento describe la arquitectura y el comportamiento del subsistema de aplicación de configuración remota en tiempo de ejecución para GEO CAM Edge (`internal/remoteconfig`).

## Contrato de Aplicación (Applier)

El paquete `internal/remoteconfig` implementa la interfaz `Applier`:

```go
type Applier interface {
    Validate(cfg Config) error
    Apply(ctx context.Context, cfg Config) error
    Rollback(ctx context.Context) error
    CurrentConfig() Config
}
```

El ciclo de ejecución de `Apply` sigue un protocolo transaccional estricto en 6 pasos:
1. **Validar nueva config**: validación exhaustiva previa a cualquier mutación de runtime. Si cualquier campo es inválido, se rechaza de inmediato (política de todo o nada: configuración parcial nunca queda activa).
2. **Conservar config anterior**: se toma una instantánea del estado activo actual para posibilitar la reversión.
3. **Detener/aplicar en pipeline de forma controlada**: se aplican los cambios a los componentes en vivo (hot-reload o reinicio controlado por cámara).
4. **Iniciar nueva config**: arranque de pipelines o sinks correspondientes con la nueva configuración.
5. **Health check**: verificación de salud operativa tras la transición (pipelines no deben estar en estado `"error"`).
6. **Rollback automático si falla**: si el health check falla, el runtime restaura de forma automática e inmediata la configuración y pipelines previos, retornando un error explicativo.

La función `Rollback(ctx)` también puede ser invocada de forma explícita por orquestadores o integraciones superiores (como IA1).

---

## Cambios Hot-Reload vs Reinicio de Pipeline

Para minimizar disrupciones en la captura de video y el consumo de CPU, las modificaciones de configuración se segregan técnicamente:

### 1. Hot-Reload (Sin reinicio de pipeline ni decodificador)
- **FPS (`TargetFPS` global o por cámara)**:
  - Se aplica dinámicamente sobre la primitiva de muestreo `processing.Sampler` (`sampler.SetTargetFPS(fps)`).
  - La actualización es thread-safe y sin bloqueos de I/O.
  - El decodificador FFmpeg continúa ejecutándose sin interrupciones ni pérdida de sincronismo RTP.
  - La frecuencia de salida (`ShouldEmit`) se ajusta inmediatamente.

### 2. Reinicio Controlado de Pipeline (Por cámara afectada)
- **Resolución de Procesamiento (`OutputWidth` / `OutputHeight`)**:
  - Modifica las dimensiones del frame procesado y redimensionado por el pipeline.
  - Requiere detener el pipeline de la cámara afectada de forma ordenada (`pipeline.Stop()`), cerrando el decodificador anterior y liberando descriptores.
  - Inicia un nuevo pipeline con las nuevas dimensiones y valida el estado saludable.
  - Afecta **únicamente a la cámara configurada**, nunca reinicia el agente completo ni pipelines de otras cámaras.
- **Regiones de Interés (`HybridROIs`)**:
  - Aplica nuevas coordenadas normalizadas `[0..1]` para la detección de movimiento en modo híbrido.
  - En reinicios controlados, reconfigura el detector de movimiento y resetea la máscara espacial.
- **Modo de Procesamiento (`ProcessingMode`)**:
  - Transición entre `cloud`, `hybrid` y `edge`:
    - `cloud → hybrid`: habilita el evaluador de movimiento local (`MotionDetector`), mantiene el sumidero `CloudSink` para los frames candidatos y marca los frames con `ProcessingModeHybrid`.
    - `hybrid → edge`: desactiva el evaluador híbrido, remueve `CloudSink` del enrutador de video, activa el worker de visión local (`vision.Worker`) y enruta los frames a `VisionSink`.
    - `edge → cloud`: detiene el worker de visión, remueve `VisionSink`, reactiva `CloudSink` y transmite todos los frames muestreados.
  - Nunca deja dos modos incompatibles operando simultáneamente.

---

## Semántica de Resolución

> [!IMPORTANT]
> `OutputWidth` y `OutputHeight` corresponden estrictamente a la resolución del **frame procesado / redimensionado** por el pipeline de software (`processing.Resize`).
> NO modifican la resolución del sensor físico ni el stream de la cámara IP (que es potestad de la configuración ONVIF / perfil de hardware).

**Reglas de validación técnica:**
- Ambas dimensiones deben ser mayores a 0.
- Deben ser números pares (requerimiento técnico de los planos de crominancia en formato `yuv420p`).
- No pueden exceder los límites técnicos máximos soportados por el pipeline: `1920x1080`.

---

## Modelos Soportados y Seguridad

En el Hito O, el Edge solo permite seleccionar y activar modelos de inferencia productivos previamente conocidos y homologados:

- **Personas**: `yolo11s-pose.pt`
- **Vehículos**: `yolo11n.pt`

### Restricciones de Seguridad:
- **NO se permiten URLs arbitrarias** (ej. `http://...`, `https://...`).
- **NO se permiten rutas arbitrarias del sistema de archivos** (ej. `/etc/...`, `/tmp/...`, traversal con `..`).
- **NO se permite el envío de pesos o binarios** dentro de la configuración remota (la distribución OTA de artefactos pertenece a otro hito).
- Cualquier modelo no reconocido es rechazado durante `Validate` antes de tocar el runtime.

---

## Restricciones sobre Cámaras y Campos Prohibidos

- **Identidad operativa**: Las cámaras se direccionan exclusivamente por su `candidate_key` operativo conocido por el runtime (`rtsp.Manager` y `processing.Manager`). Cámaras desconocidas son rechazadas en validación.
- **Campos Prohibidos**:
  Se rechaza categóricamente cualquier intento de enviar como configuración remota:
  - `tenant_id`
  - `organization_id`
  - `site_id`
  - `rtsp_url` o `rtsp_path` arbitrarios
  - Contraseñas o credenciales en texto plano (`password`, `username`)
  - Rutas del filesystem (`data_dir`, `models_dir`, `worker_cmd`, `path`)
  La presencia de cualquiera de estos campos invalida la solicitud por completo.
