---
title: Marque blanche
description: Changer le titre, la devise, le logo et la palette de l'interface grâce à un seul fichier YAML.
group: Exploitation
order: 17
slug: white-label
---

L'interface est personnalisable par un fichier. Les couleurs remplacent les
variables CSS **à l'exécution**, donc changer le thème d'un client ne recompile
rien.

## Le fichier

Copiez `brand.example.yaml` vers `brand.yaml` à la racine, ou pointez
`BREVIS_BRAND_FILE` dessus :

```yaml
titulo: Acme Data
subtitulo: Data platform

frase: |
  Measure before deciding.
  Decide before scaling.

cores:
  fundo:    "#f4efe4"
  tinta:    "#21180f"
  destaque: "#aa8450"
```

**Chaque champ est facultatif.** Ce que vous omettez garde la valeur par défaut :
un fichier avec seulement `titulo:` est donc déjà valide.

Le saut de ligne dans `frase` est préservé : il donne son rythme à la phrase dans
la barre latérale.

## Logo

```yaml
logo: https://example.com/mark.svg
```

Accepte une URL (`https://` ou `http://`) ou un chemin interne commençant par `/`.
En son absence, il utilise le symbole intégré, qui suit la couleur du thème.

:::warning Un logo hébergé ailleurs dépend de cet hébergeur
S'il tombe, ou si le cluster n'a pas de sortie vers Internet, l'image ne se charge
pas. Le reste de l'interface continue de fonctionner, mais pesez cela dans votre
choix — le reste de l'UI, polices et bundles compris, est servi depuis le binaire
lui-même précisément pour ne pas dépendre du réseau.
:::

## Valider

Une valeur hexadécimale erronée ne se manifesterait qu'au démarrage du conteneur,
et le message arriverait par le log du pod — loin de celui qui a modifié le
fichier. D'où une sous-commande :

```bash
brevis marca brand.yaml
```

```
  ok    Acme Data · Data platform
        logo      /assets/logo.svg  (built-in symbol)
        accent    (the product's own)
        Powered by Brevis
```

Appelez-la dans la CI de l'installation et l'erreur revient dans la pull request.

:::note L'absence a des sens différents
Dans `serve`, un fichier manquant signifie « utiliser l'identité par défaut » — et
l'API démarre. Dans `marca`, un fichier manquant est une **erreur** : celui qui a
demandé à valider un chemin s'attend à ce qu'on lui dise qu'il n'existe pas.
:::

## Comportement au démarrage

Une erreur de contenu — une couleur invalide, un YAML cassé — **n'empêche pas
l'interface de démarrer**. Le processus journalise un avertissement et utilise le
thème par défaut. Arrêter l'API à cause d'une couleur serait pire que la servir
avec le thème par défaut.

```
level=WARN msg="visual identity ignored" file=brand.yaml error="brand.yaml: colour accent: \"#gggggg\" is not a hex value (#rgb, #rrggbb or #rrggbbaa)"
```

## Ce qui ne peut pas être personnalisé

La ligne **« Powered by Brevis »** du pied de page ne vient pas de la
configuration — elle vient du code.

## Sur Kubernetes

Montez le fichier depuis une ConfigMap :

```yaml
volumes:
  - name: brand
    configMap: {name: brevis-brand}
containers:
  - name: api
    volumeMounts:
      - {name: brand, mountPath: /etc/brevis}
    env:
      - name: BREVIS_BRAND_FILE
        value: /etc/brevis/brand.yaml
```
