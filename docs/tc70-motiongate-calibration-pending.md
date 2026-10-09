# TC70 MotionGate calibration — estado pendiente / handoff

> Fecha de corte: 2026-10-09
> Repositorio: `drko-dev/monitoreoedgeis`
> Objetivo de este documento: permitir que otra IA o ingeniero retome esta investigación sin repetir las pruebas físicas ya realizadas ni asumir que la calibración quedó aprobada.

## Resumen ejecutivo

La calibración de MotionGate/ADAPTIVE para la cámara TC70 **NO quedó aprobada para producción**.

La ROI experimental mejoró de forma clara el comportamiento, pero no alcanzó el objetivo definido de reposo:

- IDLE sin ROI: **23,7 %**
- IDLE con ROI experimental: **49,4 %**
- Objetivo de aceptación: **>= 90 % IDLE** con escena realmente vacía
- Activaciones del gate: de **7/min** a **2,25/min**
- Tiempo con gate activo: de **52 %** a **13 %**
- Celdas p90 / p95 / p99: de **21 / 44 / 106** a **0 / 6 / 41**

La prueba con ROI **no fue concluyente** porque hubo movimiento humano real durante la ventana medida y la escena no estuvo verdaderamente quieta.

Por esa razón:

- no se persistió la ROI;
- no se modificó código;
- no se creó commit/PR por esta calibración;
- no se hizo deploy;
- la TC70 volvió a `FIXED`;
- no quedaron overrides activos.

La cámara/escena actual no es una buena referencia para cerrar esta calibración sin una ventana realmente vacía.

---

## Estado final conocido de la TC70

Al cerrar la sesión:

- Edge binario: `20198d6`
- SHA256 reportado: `e13a4b49…`
- Estado: operativo
- Modo final: `FIXED`
- Inference: 2 FPS
- Preview: 2 FPS
- Live View: objetivo 15 FPS
- backlog: 0
- stale: 0
- evidence_failures: 0
- persist_errors: 0
- reinicios: ninguno durante la prueba
- saturación: no observada
- superseded frames durante la prueba ROI: 31
- no hubo cambios de archivo/config persistidos
- no se tocó Hikvision, RTSP, credenciales, NVR, firmware ni SaaS

No asumir que ADAPTIVE está habilitado en producción.

---

## Cómo funciona el MotionGate actual

Código de referencia analizado:

- commit/binario: `20198d6`
- archivo principal: `internal/processing/motiongate.go`

### Cálculo

Cada frame decodificado evaluado por MotionGate:

- se evalúa aproximadamente a 15 FPS, independiente del FPS de inferencia;
- se reduce a una grilla fija de **32 x 18 = 576 celdas**;
- se usa luma media;
- se toma 1 de cada 2 píxeles;
- cada celda se compara contra un fondo EMA.

Una celda se considera cambiada cuando:

```text
|luma - fondo - corrimiento_global| > max(k * ruido_celda, MinDelta)
```

Donde:

- `high` -> k = 3
- `medium` -> k = 4
- `low` -> k = 6
- `MinDelta = 4`

El ruido observado por celda tenía:

- mínimo ~0,5
- promedio ~0,55

Por lo tanto, en muchas celdas tranquilas domina `MinDelta=4`.

Consecuencia importante:

**cambiar de medium a low casi no cambia el umbral efectivo en esta escena.**

No volver a asumir que `motion_sensitivity=low` por sí sola va a resolver las falsas activaciones.

### Condición de activación del gate

El gate no se activa directamente por `motion_score`.

Condición actual:

- `MinCells = 2`
- las celdas deben ser adyacentes
- `Persist = 2` frames consecutivos (~130 ms)
- `Hold = 1 s`

Con 576 celdas, 2 celdas equivalen aproximadamente a un score de 0,0035.

### Absorción y fondo

- `StillAbsorb = 1 s`
- iluminación uniforme se compensa mediante mediana
- si cambia >60 % de la escena, el fondo se resiembra

### Controlador ADAPTIVE

Cada activación del gate mantiene 10 FPS durante:

- `IdleTimeout = 10 s`

Luego baja usando la rampa del controlador.

Valores usados durante la investigación:

- idle: 2 FPS
- active: 10 FPS
- max: 15 FPS

---

## Parámetros configurables y fijos

### Configurables actualmente

- `motion_sensitivity`
- `idle_timeout_s`
- ROIs mediante el mecanismo de ROI
- también existe soporte de ROI remota vía `SetROIs`

### Fijos actualmente en código

- `MinDelta`
- `MinCells`
- `Persist`
- `Hold`
- `StillAbsorb`

El agente actualmente solo entrega `Sensitivity` al gate para este conjunto de parámetros.

---

## Medición de reposo original

Ventana aproximada:

- 21 minutos
- 18.817 frames evaluados
- 631 muestras distintas

Distribución:

| Métrica | min | p50 | p90 | p95 | p99 | max |
|---|---:|---:|---:|---:|---:|---:|
| Puntaje | 0 | 0 | 0,036 | 0,076 | 0,184 | 0,314 |
| Celdas | 0 | 0 | 21 | 44 | 106 | 181 |

Resultados:

- activaciones del gate: **147**
- frecuencia: **~7/min**
- gate activo: **52 % del tiempo**
- no hubo compensaciones globales de luz
- no aparecieron nuevos objetos relevantes para YOLO

Cantidad de muestras que pasarían distintos mínimos de celdas:

| Celdas exigidas | >=2 | >=4 | >=8 | >=12 | >=20 |
|---|---:|---:|---:|---:|---:|
| Muestras | 249 | 183 | 134 | 109 | 70 |

Conclusión de esa medición:

**la escena contiene movimiento visual real; no es solamente ruido de compresión.**

---

## Fuentes visuales de falsas activaciones identificadas

En la escena se identificaron como candidatos claros:

1. reloj OSD arriba a la izquierda;
2. monitor/pantalla abajo a la izquierda con contenido cambiante;
3. ventana con luz natural;
4. ventilador;
5. personas entrando parcialmente en cuadro.

Los picos de 100-181 celdas son compatibles con movimiento real en la imagen.

La separación asumida previamente entre “ruido 0,003-0,07” y “movimiento 0,1-0,4” quedó descartada porque las distribuciones se superponen.

No volver a usar ese umbral como criterio de diseño.

---

## ROI experimental aplicada

La ROI se probó usando:

```text
GEOCAM_VIDEO_HYBRID_ROI
```

En modo Edge, el gate puede tomar esa variable como respaldo cuando no tiene ROIs propias.

### Semántica importante

Las ROIs definen la **zona evaluada**, no la zona excluida.

Por eso se construyó el complemento de las áreas que queríamos ignorar.

Valor experimental usado:

```text
0.375,0,1,1;0,0.1111,0.375,0.4444;0.09375,0.4444,0.375,1
```

### Zonas excluidas sobre la grilla 32x18

#### Reloj OSD

- columnas 0-11
- filas 0-1

#### Monitor

- columnas 0-2
- filas 8-17

### Ventana

No fue excluida porque queda detrás de una zona donde puede aparecer una persona real.

Excluirla puede degradar detección útil.

### Alcance de la ROI

La ROI experimental afecta solamente al gate de movimiento.

**YOLO sigue analizando el cuadro completo.**

---

## Resultado de la prueba ROI

Duración aproximada:

- 12 minutos

Comparación:

| Métrica | Sin ROI | Con ROI |
|---|---:|---:|
| IDLE del controlador | 23,7 % | 49,4 % |
| Activaciones del gate | 7/min | 2,25/min |
| Tiempo gate activo | 52 % | 13 % |
| Celdas p90 / p95 / p99 | 21 / 44 / 106 | 0 / 6 / 41 |

Durante esa prueba:

- FPS asignado/efectivo: 2 a 10
- media: 5,6 FPS
- Preview: 2 FPS
- Live View: objetivo 15 FPS
- backlog: 0
- stale: 0
- evidence_failures: 0
- errores: ninguno
- reinicios: ninguno
- superseded: 31 frames

### Por qué NO se aprobó

Porque la escena no estuvo vacía.

Se registró al menos:

- una persona cruzando a las 15:40:32
- episodios de 19 a 128 celdas, compatibles con movimiento humano real

También había cambiado el encuadre respecto de una prueba anterior y ahora se veían TV y sillón.

Quedaron alrededor de 10 activaciones pequeñas dentro de 27 activaciones totales con aproximadamente 0-7 celdas.

Con una persona presente no se pudo determinar si esos blips eran falsos positivos reales.

---

## Decisión actual

La calibración queda **PAUSADA / NO APROBADA**.

No hacer ninguna de estas cosas automáticamente:

- no activar ADAPTIVE en producción;
- no persistir la ROI experimental;
- no crear una nueva ROI “parecida” sin validarla;
- no subir `MinCells` a ciegas;
- no modificar `MinDelta`;
- no modificar `Persist`;
- no modificar `Hold`;
- no modificar `StillAbsorb`;
- no pedir al usuario más pasadas físicas frente a la cámara;
- no repetir desde cero toda la investigación.

La cámara/escena usada durante esta sesión no permitió obtener una ventana de reposo limpia.

---

## Cómo retomar en el futuro

### Paso 1 — conseguir una ventana realmente vacía

Necesario:

- 10-15 minutos;
- sin personas en cuadro;
- sin intervención física del usuario;
- mantener el encuadre estable;
- usar la misma ROI experimental como punto de partida.

No pedir nuevamente al usuario que camine frente a la cámara.

### Paso 2 — medir solo reposo

Registrar como mínimo:

- porcentaje IDLE;
- activaciones/min;
- porcentaje de tiempo gate activo;
- distribución de celdas cambiadas;
- origen espacial de los blips;
- backlog;
- stale;
- evidence_failures;
- persist_errors;
- restarts;
- FPS efectivo;
- Preview;
- Live View.

### Criterio de aceptación

Si con escena realmente vacía:

```text
IDLE >= 90 %
```

y no aparecen regresiones funcionales:

**la ROI puede considerarse candidata a persistir y desplegar.**

### Paso 3 — si la ROI sigue siendo insuficiente

Si en escena vacía persisten blips de aproximadamente 0-7 celdas fuera de las zonas excluidas:

evaluar el cambio mínimo de código:

- exponer `MinCells` por configuración;
- probar un valor alrededor de **6-8**.

Motivo:

- cortaría buena parte de los blips pequeños observados;
- una persona cercana observada produjo aproximadamente **19-128 celdas**.

Riesgo:

- una persona lejana o muy pequeña puede producir pocas celdas;
- YOLO seguiría evaluando a 2 FPS en IDLE;
- pero el controlador podría no subir a 10 FPS a tiempo.

Por eso este cambio necesita validación antes de producción.

No implementar automáticamente solo porque está documentado aquí.

---

## Orden recomendado para una futura IA

Cuando otra IA retome este trabajo:

1. leer este documento completo;
2. verificar el estado actual real del repositorio y de la TC70;
3. no asumir que `20198d6` sigue siendo el binario vigente sin comprobarlo;
4. confirmar que no haya un cambio posterior ya aprobado;
5. reproducir únicamente la prueba de reposo vacía;
6. reutilizar la ROI documentada como baseline;
7. comparar contra >=90 % IDLE;
8. si pasa, preparar persistencia + PR + CI + deploy;
9. si no pasa y los blips son <=7 celdas, evaluar `MinCells` configurable;
10. mantener rollback y no mezclar esta tarea con cambios SaaS/Hikvision.

---

## Restricciones

No tocar como parte de esta calibración salvo requisito explícito independiente:

- Hikvision
- IP de cámara
- RTSP
- credenciales
- NVR
- codec
- resolución
- firmware
- billing/metering
- SaaS no relacionado

No usar un deploy productivo como entorno de experimentación.

---

## Estado de cierre

Esta tarea queda deliberadamente pendiente para retomar en otro momento con una cámara/escena adecuada para medir reposo real.

No hay un cambio ADAPTIVE/ROI aprobado esperando merge o deploy en el momento de este cierre.

La investigación técnica realizada sí deja una base suficiente para continuar sin repetir las pruebas físicas previas.
