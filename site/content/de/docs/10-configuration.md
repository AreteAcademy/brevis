---
title: Konfiguration
description: Jede Umgebungsvariable, mit Standardwert, Format und dem, was geschieht, wenn sie fehlt.
group: Referenz
order: 10
slug: configuration
---

Die gesamte Konfiguration kommt aus der **Umgebung** und wird einmal beim Start
gelesen. Danach fragt nichts mehr die Umgebung ab: Ein Prozess, dessen
Konfiguration sich mitten im Lauf ändert, ist ein Prozess, dessen Verhalten
niemand nachvollziehen kann.

## Erforderlich

| Variable | |
|---|---|
| `BREVIS_DATABASE_URL` | Postgres. Fehlt sie, ist das ein **Startfehler** in den fünf Subcommands, die die Datenbank nutzen |

## Prozess

| Variable | Standard | |
|---|---|---|
| `BREVIS_ENV` | `production` | `local` loggt als Text und lässt die Oberfläche offen; alles andere, auch wenn die Variable nicht gesetzt ist, verlangt Zugangsdaten und loggt JSON. Setze für die Entwicklung `BREVIS_ENV=local` |
| `BREVIS_HTTP_ADDR` | `:8080` | Listen-Adresse |
| `BREVIS_LOG_LEVEL` | `info` | |
| `BREVIS_SHUTDOWN_TIMEOUT_SECONDS` | `15` | Ganzzahl; ein nicht numerischer Wert ist ein Startfehler |
| `BREVIS_DB_MAX_CONNS` | Treiber-Standard, `max(4, CPUs)` | Obergrenze des Postgres-Pools jedes Prozesses; ein nicht numerischer oder negativer Wert ist ein Startfehler |
| `BREVIS_CONSOLE_STATEMENT_TIMEOUT_SECONDS` | `0` (aus) | nur die Konsole (`brevis serve`): Postgres bricht jedes ihrer Statements ab, das länger läuft, damit eine außer Kontrolle geratene Listenabfrage keine Verbindung blockiert. `30` ist der empfohlene Wert überall dort, wo der Pool Startparameter akzeptiert; standardmäßig aus, weil ein PgBouncer im Transaction-Modus die Verbindung sonst ablehnt. Wird als Startparameter der Verbindung gesendet, daher braucht ein PgBouncer im Transaction-Modus `ignore_startup_parameters = statement_timeout` |
| `BREVIS_RETENTION_TRIM_DAYS` | `0` (aus) | optionales geplantes Prune, ausgeführt von `brevis scheduler`: leert Log, Phasen und Ausgabe von Runs, die älter als dieser Wert sind. Gleiche Wirkung wie `brevis prune --trim-after`. `0` = der Scheduler räumt nie auf |
| `BREVIS_RETENTION_PURGE_DAYS` | `0` (nie) | löscht zusätzlich zum vorigen auch Runs, die älter als dieser Wert sind. Muss mindestens dem Trim-Wert entsprechen |
| `BREVIS_RETENTION_INTERVAL_HOURS` | `24` | wie oft das geplante Prune läuft (einmal beim Start, danach alle N Stunden) |
| `BREVIS_BRAND_FILE` | `brand.yaml` | Branding; fehlt sie, gilt der Standard |
| `BREVIS_UI_URL` | — | Basis des Run-Links in Alarmen |
| `BREVIS_METRICS_ADDR` | — | Port der Prometheus-Metriken; leer = nicht exponiert. Siehe [Observability](/docs/observability/) |
| `BREVIS_SLACK_WEBHOOK` | — | Ziel für Fehleralarme. Leer = niemand wird informiert |

## Authentifizierung

| Variable | |
|---|---|
| `BREVIS_AUTH_USER` | Benutzer der Oberfläche |
| `BREVIS_AUTH_PASSWORD_HASH` | Hash `pbkdf2-sha256$…`, erzeugt von `brevis hash` |
| `BREVIS_AUTH_SECRET` | 32+ Byte zum Signieren der Sitzung (`openssl rand -base64 48`) |

Alle drei gehören zusammen, oder keine.

| Variable | Standard | |
|---|---|---|
| `BREVIS_AUTH_CLIENT_IP_HEADER` | — | Header, den dein Proxy mit der Client-Adresse setzt, z. B. `X-Forwarded-For`. Verwendet wird der **letzte** Eintrag (den dein Proxy angehängt hat). Leer = die Adresse der Verbindung |

Die Anmeldung wird pro Client-Adresse gedrosselt (IPv6 nach ihrem /64): Nach drei Fehlversuchen wird die Adresse 2 Sekunden gesperrt, die Sperre verdoppelt sich bis auf 15 Minuten. **Hinter einem Ingress oder Load Balancer die Variable setzen** (das Helm-Chart tut das, wenn `api.ingress.enabled` aktiv ist; überschreiben mit `auth.clientIPHeader`): Ohne sie teilen sich alle Clients die Adresse des Proxys, sodass eine einzelne Person, die Passwörter errät, alle aussperren würde. In diesem Fall wird die Sperre außerhalb von `BREVIS_ENV=local` auf 30 Sekunden begrenzt, und beim Start wird eine Warnung geloggt. Setze die Variable nur, wenn der Header von einem Proxy geschrieben wird, dem du vertraust, und der Client ihn nicht fälschen kann. Pro Adresse läuft immer nur eine Passwortprüfung gleichzeitig; zwei Personen hinter demselben NAT, die sich in derselben Sekunde anmelden, sehen daher kurz ein 429.

:::danger Halb konfiguriert ist ein Startfehler
Wer den Benutzernamen eingetragen hat, glaubt, die Tür sei zu. Die Oberfläche
löst Pipelines aus: Ein `POST /workflows/<slug>/trigger` startet einen
`dbt build`, der ins Warehouse schreibt. Offen im Internet ist sie eine
Fernbedienung für das Warehouse – und eine Warnung im Log nimmt diesen Glauben
nicht zurück, weil niemand das Log eines Prozesses liest, der funktioniert.
:::

Außerhalb von `BREVIS_ENV=local` wird ein Start ohne Zugangsdaten verweigert:

```
error: BREVIS_ENV=production requires a credential: set BREVIS_AUTH_USER,
BREVIS_AUTH_PASSWORD_HASH (generate one with `brevis hash`) and BREVIS_AUTH_SECRET
```

:::note Die alten Namen funktionieren weiterhin
Vier Variablen wurden am 10.09.2026 umbenannt, als die letzten portugiesischen
Namen die Betreiberseite des Projekts verließen: `BREVIS_AUTH_USUARIO`,
`BREVIS_AUTH_SENHA_HASH`, `BREVIS_AUTH_SEGREDO` und
`BREVIS_POD_MANTER_EM_FALHA`. Eine Installation, die den alten Namen setzt,
funktioniert weiter und loggt eine Warnung, die den neuen nennt.

Fügen Sie den neuen Namen hinzu, deployen Sie, und entfernen Sie dann den alten –
der neue Name gewinnt, wenn beide gesetzt sind. Die alten Namen verschwinden in
einer Major-Version.

Im SDK wurde `BREVIS_SDK_LIMITE_INLINE` nach derselben Regel zu
`BREVIS_SDK_INLINE_LIMIT`.
:::

## Task-Umgebung

| Variable | Standard | |
|---|---|---|
| `BREVIS_TASK_ENV` | leer | was jeder Step erhält |

Ein Task erbt die Umgebung des Orchestrators **nicht**. Der Grund ist konkret:
Der Prozess trägt `BREVIS_DATABASE_URL` mit Benutzername und Passwort von
Postgres, und ein Workflow ist ein beliebiger Befehl, den jemand anderes
geschrieben hat – Erben würde die Datenbank-Zugangsdaten an jeden Step jeder
Pipeline weitergeben.

Was der Task braucht, wird deklariert:

```bash
BREVIS_TASK_ENV=GOOGLE_PROJECT_ID,STAGE   # forwards these two from the process
BREVIS_TASK_ENV=STAGE=prod                # sets a literal
BREVIS_TASK_ENV='*'                       # forwards everything EXCEPT BREVIS_*
```

`PATH` und `HOME` sind immer enthalten – ohne `PATH` wird kein Befehl aufgelöst,
und der Fehler wäre ein „not found“, das nichts erklärt.

Ein fehlender Name wird **nicht** zu einem leeren String: `GOOGLE_PROJECT_ID=""`
ließe dbt mit einer schlechteren Meldung scheitern als eine fehlende Variable.

## Kubernetes

Entscheidungen der **Installation**, nicht des Workflows.

| Variable | Standard | Format |
|---|---|---|
| `BREVIS_PODS` | `auto` | `auto`, `on` oder `off`; alles andere ist ein Startfehler |
| `BREVIS_POD_NAMESPACE` | der des Clusters | |
| `BREVIS_POD_SERVICE_ACCOUNT` | — | |
| `BREVIS_POD_PULL_SECRETS` | — | kommagetrennte Liste |
| `BREVIS_POD_ENV_FROM_SECRETS` | — | kommagetrennte Liste |
| `BREVIS_POD_ENV_FROM_CONFIGMAPS` | — | kommagetrennte Liste |
| `BREVIS_POD_ALLOWED_SECRETS` | — | kommagetrennte Liste |
| `BREVIS_POD_CREDENTIAL_PATH` | — | im Pod eingehängter Pfad |
| `BREVIS_POD_CREDENTIAL_PVC` | — | PVC, das die Zugangsdaten hält |
| `BREVIS_POD_NODE_SELECTOR` | — | `key=value,other=value` |
| `BREVIS_POD_TOLERATIONS` | — | `key=value:effect,…`; nur der Operator `Equal` |
| `BREVIS_POD_KEEP_ON_FAILURE` | `false` | `true` behält den Pod zur Untersuchung |

In Listen werden leere Einträge ignoriert: `a,,b` ist ein Tippfehler, und ein
leerer Secret-Name ließe den Server den ganzen Pod ablehnen.

## Tests

| Variable | |
|---|---|
| `BREVIS_TEST_DATABASE_URL` | Datenbank für Integrationstests; ohne sie werden sie übersprungen |

## Health-Endpoints

| | |
|---|---|
| `GET /health` | Liveness – fragt die Datenbank **nicht** ab |
| `GET /ready` | Readiness – fragt ab und nennt die Abhängigkeit, die ausgefallen ist |

Die Trennung ist Absicht: Eine Liveness-Probe, die von einem externen Dienst
abhängt, lässt Kubernetes den **Pod killen**, wenn die Datenbank wackelt, statt
ihn nur aus dem Load Balancer zu nehmen.

## Ein vollständiges Beispiel

```bash
# required
export BREVIS_DATABASE_URL='postgres://brevis:secret@db:5432/brevis?sslmode=require'

# production requires credentials
export BREVIS_ENV=production
export BREVIS_AUTH_USER=operator
export BREVIS_AUTH_PASSWORD_HASH="$(brevis hash < password.txt)"
export BREVIS_AUTH_SECRET="$(openssl rand -base64 48)"

# what the tasks need, and nothing more
export BREVIS_TASK_ENV=GOOGLE_PROJECT_ID,STAGE,DBT_KEYFILE

# pods
export BREVIS_PODS=on
export BREVIS_POD_NAMESPACE=data
export BREVIS_POD_SERVICE_ACCOUNT=brevis-runner
export BREVIS_POD_ENV_FROM_SECRETS=bigquery-cred

# alerts
export BREVIS_SLACK_WEBHOOK='https://hooks.slack.com/services/...'
export BREVIS_UI_URL='https://brevis.example.com'
```
