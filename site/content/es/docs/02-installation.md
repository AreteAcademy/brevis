---
title: Instalación
description: Cómo obtener el binario, qué requiere y cómo confirmar que la instalación es correcta.
group: Primeros pasos
order: 2
slug: installation
---

brevis.sh se distribuye como un **único binario** y como una **imagen de Docker**. No hay
ningún runtime que instalar junto a él: las migraciones, los assets de la interfaz y las fuentes viven
dentro del binario.

## Requisitos

| | |
|---|---|
| **PostgreSQL** | necesario para `serve`, `scheduler`, `migrate`, `publish` y `backfill` |
| **Go 1.25+** | solo si se compila desde el código fuente |
| **Kubernetes** | opcional: sin clúster, los steps corren como procesos locales |

`brevis run`, `validate`, `marca`, `hash` y `version` **no** necesitan base de datos.

## Compilar desde el código fuente

Este es el camino que produce un binario con la versión y el commit incorporados:

```bash
git clone https://github.com/AreteAcademy/brevis.git
cd brevis
make build
./bin/brevis version
```

```
brevis 0.11.2
  commit  bb832ff
  build   2026-09-05T12:00:00Z
  go      go1.25.7 darwin/arm64
```

:::warning El engine todavía no tiene una release etiquetada
Todos los tags del repositorio pertenecen al SDK (`sdk/v*`); el módulo raíz no tiene
ninguno. Un `go install github.com/AreteAcademy/brevis/cmd/brevis@latest` se resuelve
al último commit de la rama y produce un binario que se identifica
como `brevis dev`, sin commit ni fecha: los `-ldflags` solo entran
mediante `make build` y `docker build`. Para una versión trazable, usar la imagen
o `make build`.
:::

## Docker

Dos imágenes del mismo binario, porque los dos roles tienen necesidades opuestas:

| tag | base | por qué |
|---|---|---|
| `:0.11.2` | distroless | la API solo sirve HTTP y no ejecuta nada: sin shell, superficie mínima |
| `:0.11.2-worker` | alpine + tini | los steps `run:` necesitan una shell |

```bash
docker run --rm areteacademy/brevis:latest version
```

Ambas imágenes están en [hub.docker.com/r/areteacademy/brevis](https://hub.docker.com/r/areteacademy/brevis),
para `amd64` y `arm64`.

## Un entorno local completo

El repositorio incluye un `docker-compose.yml` que levanta Postgres, la API y
el scheduler:

```bash
make up      # Postgres + API + scheduler
make smoke   # checks /health and /ready
make logs
make down
```

La interfaz está en `http://localhost:8080`.

## Configuración mínima

Solo se requiere una variable:

```bash
export BREVIS_DATABASE_URL='postgres://brevis:brevis@localhost:5432/brevis?sslmode=disable'
```

Sin ella, los subcomandos que usan la base de datos fallan al arrancar, a propósito. Un
proceso que arranca sin credenciales solo descubre el problema en la
primera petición, y para entonces el readiness ya le mintió al orquestador.

```
$ brevis migrate status
erro: BREVIS_DATABASE_URL e obrigatoria
```

La lista completa está en [Configuración](/docs/configuration/).

## Aplicar el esquema

Las migraciones están incorporadas en el binario y se aplican con su propio subcomando;
**`serve` nunca altera el esquema**:

```bash
brevis migrate up
brevis migrate status
```

## Confirmar

```bash
brevis version
brevis validate examples/
```

```
  ok    daily_analytics              dag  5 steps, 5 dependencias  (manual)
  ok    daily-report                 chain  3 steps, 2 dependencias  cron 0 2 * * *
  ok    hello                        dag  4 steps, 4 dependencias  (manual)
```

Si ambos responden, la instalación es correcta. Continúa con el
[Inicio rápido](/docs/quickstart/).
