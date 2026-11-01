---
title: Introducción
description: Qué es brevis.sh, el problema que resuelve y cuándo no es la elección adecuada.
group: Primeros pasos
order: 1
slug: introduction
---

brevis.sh es un **runtime de orquestación de datos escrito en Go**. Un único binario
reúne lo que normalmente requiere cuatro herramientas: transformación
declarativa, orquestación de workflows, una cola persistente, un scheduler y una
interfaz para operar todo eso.

Es de código abierto bajo la licencia MIT, y el proyecto nace en
[Aretê Academy](https://areteacademy.com.br/).

## El problema

Un stack de datos común combina una herramienta de transformación, un orquestador, una cola y
una interfaz. Cada pieza es buena en lo suyo, y la integración entre
ellas se vuelve trabajo permanente: cuatro esquemas de versionado, cuatro formas de configurar,
cuatro lugares donde mirar cuando algo falla.

Ese costo nunca aparece en un diagrama de arquitectura. Aparece en el tiempo que un
equipo dedica a mantener el pegamento en lugar de trabajar en el dominio que solo
ese equipo conoce.

## La propuesta

Un artefacto, con el subcomando que exige cada rol:

```bash
brevis serve        # API + interface
brevis scheduler    # materialises schedules and drains the queue
brevis run wf.yaml  # runs now, no database
```

Un workflow es un archivo YAML versionado. Revisar un pipeline pasa a ser revisar un
diff, no una sesión de clics.

```yaml
name: ingest
schedule: "0 5 * * *"

steps:
  - id: bronze
    image: registry.example/dbt:1.10.3
    run: dbt build --select bronze+

  - id: notify
    image: ghcr.io/example/notify:0.3
    run: /notify --channel data
    depends_on: [bronze]
```

## Qué lo hace diferente

**Cada step corre en su propio pod, con su propia imagen.** No hay un worker
genérico esperando trabajo: el trabajo trae su propio runtime. Un step de `dbt` arranca
con la imagen de dbt; un fetcher en Go a su lado arranca con 5,8 MB y 32Mi de
memoria, en lugar de heredar el tamaño de su vecino más grande.

**El scheduler crea runs; la cola los ejecuta.** Dos bucles independientes,
y esa separación es deliberada: uno puede caerse sin interrumpir al
otro, y reprocesar un mes pasado no depende del reloj.

**La validación no necesita base de datos.** `brevis validate` corre en CI junto a
las pruebas, de modo que un error de YAML falla en el pull request y no en el clúster.

## Tres piezas, separadas a propósito

| pieza | qué es | versión |
|---|---|---|
| **engine** | `brevis`, el binario del que trata esta página: orquesta, programa, ejecuta y opera | `0.15` |
| **SDK** | un módulo Go para escribir el step que extrae y carga | `0.66` |
| **gateway** | un endpoint HTTP que aterriza datos que *llegan*, en lugar de ir a buscarlos | `0.8` |

Las versiones son independientes porque las madureces lo son. Numerar el gateway
`0.15.3` porque el engine está en esa versión afirmaría una madurez que no tiene,
y una versión no se puede despublicar.

Lo que une a las tres es un número: el `ingestion_id`, un UUID v5 congelado
sobre `provider|entity|source_key|record_ts`. Una fila que aterriza el
[gateway](/docs/ingestion/) y una fila que aterriza un fetcher del [SDK](/docs/sdk/)
para el mismo registro son *una sola fila*, sin reconciliación entre ellas.

## Cuándo brevis.sh no es la opción

Documentar lo que una herramienta no hace ahorra más tiempo que documentar lo que
hace:

- **Ya usas Airflow o Dagster y estás conforme.** Migrar cuesta más que
  lo que se ahorra al operar un binario menos.
- **Tus workflows no tratan de datos.** brevis.sh asume DAGs de
  transformación y carga; un orquestador de propósito general te sirve mejor.
- **Necesitas un catálogo de operadores listos.** Aquí un step es una imagen y
  un comando. Eso es simple, y es limitado: no hay cientos de
  integraciones que arrastrar.
- **Tu equipo no usa Kubernetes y no piensa usarlo.** El modo local
  funciona, pero el diseño asume pods.

## Código abierto. Elige cómo participar.

brevis.sh es 100% de código abierto. Sin licencias propietarias, sin edición limitada, sin candados sobre lo que construyes.

### 100% abierto

*código · licencia de código abierto*

Lee, usa, adapta y mejora el runtime en tu propia operación. Runtime completo · Código auditable · Contribuciones abiertas.

[Ver el repositorio](https://github.com/AreteAcademy/brevis)

### Construir juntos

*comunidad · issues abiertos* · **Camino principal**

Participa en las conversaciones que deciden en qué se convierte el proyecto. Issues y propuestas · Intercambio entre pares · Roadmap público.

[Seguir el proyecto](https://github.com/AreteAcademy/brevis/issues)

### Aprender a fondo

*aretê · tecnología con propósito*

Conoce la escuela que conecta el pensamiento filosófico y la ingeniería de software. Contenido original · Aprendizaje continuo · Canal abierto.

[Visitar Aretê](https://areteacademy.com.br/)

## Adónde ir después

| si quieres | ve a |
|---|---|
| instalarlo y verlo funcionar | [Instalación](/docs/installation/) y [Inicio rápido](/docs/quickstart/) |
| entender el formato de los workflows | [Workflows](/docs/workflows/) |
| entender la arquitectura | [Scheduler y cola](/docs/scheduler-and-queue/) |
| la lista de comandos y flags | [CLI](/docs/cli/) |
| escribir un fetcher en Go | [SDK](/docs/sdk/) |
| pasar un valor de un step al siguiente | [Contexto entre steps](/docs/context/) |
| escribir un step en Python | [Bibliotecas cliente](/docs/libraries/) |
| recibir datos por HTTP en lugar de obtenerlos | [Ingesta](/docs/ingestion/) |
| construir dashboards y alertas | [Observabilidad](/docs/observability/) |

:::note Sobre el nombre
*Brevis* es latín para «corto, breve», la raíz de *brevedad*. Viene del
**De Brevitate Vitae** de Séneca: no es que tengamos poco tiempo, sino que
perdemos mucho.
:::
