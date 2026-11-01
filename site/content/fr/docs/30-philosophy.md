---
title: Philosophie
description: D'où vient brevis.sh : Sénèque, Aretê Academy, et ce que cela décide dans le code.
group: Le projet
order: 30
slug: philosophy
serif: true
---

brevis.sh est né au sein d'Aretê Academy : discipline, finalité et excellence appliquées à la construction de logiciels.

## De brevitate vitae

> Ce n'est pas que nous avons peu de temps, c'est que nous en perdons beaucoup.

La maxime de Sénèque guide la question derrière brevis.sh : quelles complexités méritent vraiment le temps d'une équipe ? Chaque workflow s'exécute dans des pods isolés afin que les échecs, les versions et les dépendances aient des frontières claires.

Quatre choix du runtime découlent de cette question :

- **image par step** — Chaque step déclare le runtime dont il a besoin. Monter une version n'oblige pas à toucher aux autres.
- **logs par run** — La sortie appartient au run qui l'a produite. Enquêter sur un échec, ce n'est pas reconstruire un contexte perdu.
- **nouvelles tentatives persistantes** — La tentative survit au processus. Un redémarrage n'efface pas ce qui était déjà connu du travail.
- **isolation des dépendances** — Ce qu'un step transporte ne déborde pas chez ses voisins. Une frontière claire est ce qui rend un échec lisible.

## Ce que le projet propose

Entre la *philosophie sans pratique* et la *technologie sans direction*, **la clarté pour faire ce qui compte.**

## Les outils façonnent notre façon de penser.

> « Le code durable n'est pas simplement du code qui continue de tourner. C'est du code qui laisse de la place au raisonnement des personnes. » — La philosophie appliquée au logiciel

> « La clarté n'est pas l'opposé de la sophistication. C'est le critère qui empêche la sophistication de devenir du bruit. » — L'open source par principe
