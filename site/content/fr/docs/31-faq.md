---
title: FAQ
description: Les questions qui reviennent en premier, avec des réponses courtes et où aller ensuite.
group: Le projet
order: 31
slug: faq
---

## brevis.sh est-il vraiment 100 % open source ?

Oui. Le runtime complet est ouvert à la lecture, à l'utilisation, à l'adaptation et à la contribution.

## Que signifie la référence à Sénèque pour le projet ?

De Brevitate Vitae inspire une ingénierie plus intentionnelle : réduire la complexité qui consomme du temps, et préserver de la place pour un travail qui a un sens.

Plus de détails dans [Philosophie](/docs/philosophy/).

## Comment fonctionne l'isolation par pod ?

Chaque step de workflow s'exécute comme son propre pod avec sa propre image, ce qui garde séparés les dépendances et le contexte d'exécution.

Plus de détails dans [Un pod par step](/docs/pod-per-step/).

## Quelle est la relation avec Aretê Academy ?

brevis.sh est un projet d'Aretê Academy, qui rapproche les grandes idées de la philosophie des défis réels de la construction de logiciels.

## Puis-je n'utiliser qu'un seul des composants ?

Oui. Chaque composant a son propre module, sa version et son image : le SDK est une bibliothèque Go qui s'exécute dans n'importe quel programme, le Gateway démarre seul avec un fichier YAML et sans base de données, et brevis-sql s'exécute comme un conteneur. Le Core orchestre les autres quand vous le souhaitez — il n'est un prérequis pour aucun d'eux.

Plus de détails dans [Écosystème](/docs/ecosystem/).
