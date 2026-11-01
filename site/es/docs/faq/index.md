# FAQ

> Las preguntas que surgen primero, con respuestas breves y adónde ir después.

*https://brevis.sh/es/docs/faq/ · brevis.sh docs (es)*

---

## ¿brevis.sh es realmente 100% de código abierto?

Sí. El runtime completo está abierto para leerlo, usarlo, adaptarlo y contribuir.

## ¿Qué significa la referencia a Séneca para el proyecto?

De Brevitate Vitae inspira una ingeniería más intencional: reducir la complejidad que consume tiempo y preservar espacio para el trabajo con propósito.

Más en [Filosofía](/es/docs/philosophy/index.md).

## ¿Cómo funciona el aislamiento por pod?

Cada step de un workflow corre como su propio pod con su propia imagen, manteniendo separadas las dependencias y el contexto de ejecución.

Más en [Pod por step](/es/docs/pod-per-step/index.md).

## ¿Cuál es la relación con Aretê Academy?

brevis.sh es un proyecto de Aretê Academy, que acerca las grandes ideas de la filosofía a los desafíos reales de construir software.

## ¿Puedo usar solo una de las piezas?

Sí. Cada pieza tiene su propio módulo, versión e imagen: el SDK es una biblioteca Go que corre en cualquier programa, el Gateway arranca por sí solo con un archivo YAML y sin base de datos, y brevis-sql se ejecuta como contenedor. El Core orquesta a las demás cuando se quiere; no es requisito de ninguna.

Más en [Ecosistema](/es/docs/ecosystem/index.md).
