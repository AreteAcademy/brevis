---
title: Observabilidad
description: Métricas de Prometheus de ambos procesos, qué vigilar y qué está ausente deliberadamente.
group: Operación
order: 16
slug: observability
---

Brevis expone métricas de Prometheus en un puerto propio, desde **ambos** procesos.

```
GET http://<pod>:9090/metrics
```

El puerto viene de `BREVIS_METRICS_ADDR`. Está separado del puerto HTTP a
propósito: las métricas no pasan por la autenticación de la interfaz, y servir
ambos desde un mismo lugar obligaría a elegir entre un scrape autenticado y un
dashboard abierto.

## Dos procesos, dos endpoints

Esta es la parte que la mayoría de las instalaciones hace mal, al scrapear solo la API.

| | sirve | informa |
|---|---|---|
| `brevis serve` | la UI y la API HTTP | profundidad de la cola |
| `brevis scheduler` | nada por HTTP | profundidad de la cola, latencia de reclamo, slots, huérfanos, runs, steps |

El scheduler es quien ejecuta, así que casi todo viene de él. Un dashboard
construido solo sobre la API muestra la cola creciendo y **nada** sobre lo que la
está vaciando.

## Las métricas que publica un step

Además de lo que mide el engine, un step puede publicar las suyas: cuántas filas
aterrizaron, cuántas rechazó el proveedor:

```python
from brevis import metrics
metrics.set("rows_loaded", 48213)
```

Aparecen en el **mismo** `/metrics` del scheduler, con prefijo y etiquetadas por el
engine:

```
brevis_step_rows_loaded{workflow="daily_sales",step="load"} 48213
```

El step no abre ningún puerto: escribe una línea en stdout y el engine la registra;
un pod que vive cuarenta segundos no se puede scrapear. Ver [Python](/docs/python/).

## Qué vigilar

| señal | por qué |
|---|---|
| la profundidad de la cola sube sin bajar | el scheduler murió, o `--concurrency` es bajo para la carga |
| la latencia de reclamo crece | contención en la base de datos, o una cola demasiado grande para el intervalo |
| huérfanos > 0 | runs reclamados por un proceso que murió antes de terminar |
| slots sin materializar | el bucle del scheduler se detuvo, y las programaciones se detienen con él |

Las dos primeras por sí solas no distinguen «carga alta» de «proceso muerto», y por eso
los slots importan. Una cola que sube **con** slots creándose es carga; una cola que sube
**sin** slots es un bucle detenido.

## Kubernetes

```yaml
env:
  - name: BREVIS_METRICS_ADDR
    value: ":9090"
ports:
  - name: metrics
    containerPort: 9090
```

Con el Prometheus Operator, un `PodMonitor` por rol, y sí tiene que ser
uno por rol, porque los dos Deployments llevan etiquetas distintas e informan
conjuntos distintos.

## Qué está ausente deliberadamente

**Trazas.** Todavía no hay OpenTelemetry. Un step corre como su propio pod, y lo que
importa de él —inicio, fin, intento, código de salida, log— ya está en la
base de datos y en la pantalla. La traza útil sería la de dentro del step,
y esa pertenece al código del step, no al orquestador.

Eso se sostiene mientras el step sea la unidad. El día en que un step llame a otro servicio y
la pregunta pase a ser «¿adónde se fueron los 40 segundos?», la traza empezará a valer
lo que cuesta.

## Siguientes pasos

- [Kubernetes](/docs/kubernetes/) — desplegar ambos procesos
- [Configuración](/docs/configuration/) — `BREVIS_METRICS_ADDR` y el resto
