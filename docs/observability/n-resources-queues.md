# Hito N — Observabilidad Edge (Recursos, Colas y Confiabilidad RTSP)

Este documento describe la especificación, nombres, unidades y semántica del slice de observabilidad implementado en el Gateway Edge (Milestone N).

## 1. Métricas de Host y Recursos (`resources`)

Expuestas en `GET /status` bajo la clave `resources`. Ningún valor ausente o no medible se transforma en un cero falso: campos opcionales no medibles se serializan como `null` o se omiten (`omitempty`).

### CPU (`cpu`)
- **`percent`**: Porcentaje de utilización global de CPU en el intervalo entre muestras (`[0.0..100.0]`).
- **Primitiva**: Calculado a partir de deltas de `/proc/stat` mediante `platform.CPUSampler` (con protección de concurrencia).
- **Semántica de degradación**: La primera llamada devuelve `nil` (fase de priming). En plataformas sin procfs (ej. macOS/Windows), se reporta `nil` (nunca cero falso).

### Memoria RAM (`memory`)
- **`total_bytes`**: Memoria física total del host en bytes (`uint64`), leída de `MemTotal` en `/proc/meminfo` (o fallback estático de sistema).
- **`available_bytes`**: Memoria disponible real en bytes (`uint64`), calculada a partir de `MemAvailable` (incluye caches y buffers recuperables).
- **`used_bytes`**: Memoria efectivamente usada (`total_bytes - available_bytes`).
- **`used_percent`**: Porcentaje de uso respecto al total (`(used_bytes / total_bytes) * 100.0`).
- **Semántica sin cero falso**: En sistemas donde `MemAvailable` no está expuesto (ej. macOS dev), `used_bytes`, `available_bytes` y `used_percent` permanecen como punteros `nil`.

### Almacenamiento en Disco (`disk`)
- **`data_dir`**: Directorio raíz efectivo observado que aloja `GEOCAM_DATA_DIR` (obtenido resolviendo ancestros existentes, sin exponer rutas sensibles).
- **`total_bytes`**: Capacidad total del sistema de archivos en bytes.
- **`available_bytes`**: Espacio disponible real para escritura por procesos sin privilegios de root (`bavail * bsize` de `statfs`).
- **`used_bytes`**: Espacio consumido en bytes (`(blocks - bfree) * bsize`).
- **`used_percent`**: Porcentaje de utilización del disco (`(used_bytes / total_bytes) * 100.0`).
- **Alineación con Full Edge**: `PlatformDiskChecker.FreeBytes` utiliza prioritariamente `available_bytes` para validar umbrales antes de persistir evidencias.

### Térmico (`thermal`)
- **`temperature_c`**: Temperatura de la zona térmica más caliente en grados Celsius (`float64`).
- **Semántica**: Leída de `/sys/class/thermal/thermal_zone*/temp`. Si el host no tiene sensores térmicos (ej. máquinas virtuales o contenedores), el campo es `nil` y no degrada el estado del agente.

---

## 2. Métricas de Colas y Backpressure (`queues`)

Las métricas de encolamiento se presentan segregadas por componente real bajo la clave `queues`. Nunca se agregan como una única cola para evitar ocultar cuellos de botella específicos.

### Processing Router (`router`)
Lista de estados de cola por cada `Sink` registrado:
- **`name`**: Nombre identificador del sink (ej. `"debug"`, `"cloud"`, `"vision"`).
- **`depth`**: Cantidad de frames actualmente en el canal buffer (`len(chan)`).
- **`capacity`**: Capacidad máxima configurada del canal buffer (`cap(chan)`).
- **`drops`**: Contador acumulativo de frames descartados por cola llena (`drops`).

### Offline Cloud Buffer (`cloud_buffer`)
Estado del buffer de reintento en disco para envíos Cloud (Milestone I6):
- **`name`**: `"cloud_buffer"`.
- **`depth`**: Cantidad de frames actualmente esperando en cola (`buffered_frames`).
- **`capacity`**: Límite máximo de frames en buffer (`max_frames`).
- **`drops`**: Frames descartados por buffer lleno (`dropped_buffer_full`) o por exceso de tamaño permanente (`dropped_oversize`).
- **`oldest_pending`**: Timestamp ISO del frame más antiguo esperando en cola.
- **`degraded`**: `true` si se han registrado descartes por saturación.

### Local Event Backlog (`edge_backlog`)
Estado de la cola persistente de eventos y evidencias locales (Milestones K/M):
- **`name`**: `"edge_backlog"`.
- **`depth`**: Cantidad de operaciones pendientes en cola (`backlog_count`).
- **`capacity`**: Límite de operaciones configuradas (`max_operations`).
- **`drops`**: Eventos descartados al alcanzar la capacidad máxima (`drops`).
- **`oldest_pending`**: Timestamp del evento más antiguo en cola.
- **`degraded`**: Indicador booleano de degradación operativa.
- **`quarantined`**: Cantidad de eventos derivados a cuarentena por errores irrecuperables.

### Inferencia Vision (`vision`)
Estado del pipeline de inferencia YOLO local (Milestone K):
- **`name`**: `"vision"`.
- **`depth`**: Inferencia en vuelo actual (`in_flight_inference`).
- **`capacity`**: Profundidad máxima permitida del semáforo/cola (`queue_depth`).
- **`drops`**: Tareas de inferencia descartadas por saturación (`queue_dropped`).
- **`degraded`**: `true` si existe presión de memoria (`memory_pressure`), saturación de disco (`disk_saturated`) o fallo del worker.

---

## 3. Confiabilidad y Fallos RTSP (`cameras`)

Supervisión de cada stream RTSP vía `rtsp.Supervisor` expuesta en `cameras`:

- **`reconnect_count`**: Contador estrictamente monótono de reconexiones intentadas. Nunca se resetea a cero al reconectar o fallar.
- **`packets_received` / `bytes_received`**: Volumen de tráfico recibido del stream.
- **`last_packet_at`**: Timestamp UTC del último paquete de red procesado.
- **`timeout_count` / `stall_count`**: Contador acumulativo de silencios o interrupciones por timeout en el socket RTSP (`PacketTimeout`).
- **`last_error_safe`**: Último error sanitizado.
  - Se eliminan credenciales en URIs (`rtsp://usuario:password@host...` -> `rtsp://[REDACTED]@host...`).
  - Se redactan parámetros sensibles en queries (`password=`, `token=`, etc.).
  - Se eliminan caracteres de control o no imprimibles.
  - Longitud máxima acotada a 255 bytes para prevenir desbordes en payloads de telemetría.
- **`status`**: Estado de conectividad de la cámara (`connecting`, `online`, `degraded`, `offline`).

---

## 4. Estado Operativo del Agente (`/status`)

- **Distinción de estado**:
  - `READY`: El agente resolvió su identidad, cargó sus credenciales y todos los módulos críticos iniciaron correctamente.
  - `DEGRADED`: Fallo crítico en identidad, credenciales corruptas o fallo no recuperable de autenticación SaaS.
- **Resiliencia ante fallos de cámaras**: Una cámara en estado `degraded` u `offline` marca únicamente el stream respectivo en la lista `cameras`, sin degradar el estado global `READY` del agente.
- **Compatibilidad**: Se preserva la totalidad de los campos preexistentes de `Snapshot` para garantizar compatibilidad retroactiva absoluta con consumidores HTTP existentes.
