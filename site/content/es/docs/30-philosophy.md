---
title: Filosofía
description: De dónde viene brevis.sh: Séneca, Aretê Academy y lo que eso decide en el código.
group: El proyecto
order: 30
slug: philosophy
serif: true
---

brevis.sh nace dentro de Aretê Academy: disciplina, propósito y excelencia aplicados a la construcción de software.

## De brevitate vitae

> No es que tengamos poco tiempo, sino que perdemos mucho.

La máxima de Séneca guía la pregunta detrás de brevis.sh: ¿qué complejidades merecen realmente el tiempo de un equipo? Cada workflow corre en pods aislados para que los fallos, las versiones y las dependencias tengan límites claros.

Cuatro decisiones del runtime salen de esa pregunta:

- **imagen por step** — Cada step declara el runtime que necesita. Subir una versión no obliga a tocar las demás.
- **logs por run** — La salida pertenece al run que la produjo. Investigar un fallo no es reconstruir un contexto perdido.
- **reintentos persistentes** — El intento sobrevive al proceso. Un reinicio no borra lo que ya se sabía del trabajo.
- **aislamiento de dependencias** — Lo que un step lleva no se filtra a sus vecinos. Un límite claro es lo que hace legible un fallo.

## Lo que propone el proyecto

Entre la *filosofía sin práctica* y la *tecnología sin dirección*, **claridad para hacer lo que importa.**

## Las herramientas moldean la manera en que pensamos.

> «El código sostenible no es solo código que sigue funcionando. Es código que deja espacio para que las personas razonen.» — Filosofía aplicada al software

> «La claridad no es lo opuesto a la sofisticación. Es el criterio que impide que la sofisticación se convierta en ruido.» — Código abierto por principio
