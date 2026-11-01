---
title: इंस्टॉलेशन
description: बाइनरी कैसे पाएँ, उसे किन चीज़ों की ज़रूरत है, और इंस्टॉल सही है यह कैसे जाँचें।
group: शुरुआत करें
order: 2
slug: installation
---

brevis.sh एक **अकेली बाइनरी** और एक **Docker image** के रूप में आता है। इसके साथ
कोई runtime इंस्टॉल नहीं करना पड़ता: migrations, इंटरफ़ेस की assets और फ़ॉन्ट सब
बाइनरी के भीतर ही हैं।

## आवश्यकताएँ

| | |
|---|---|
| **PostgreSQL** | `serve`, `scheduler`, `migrate`, `publish` और `backfill` के लिए ज़रूरी |
| **Go 1.25+** | सिर्फ़ तब, जब आप सोर्स से बिल्ड करें |
| **Kubernetes** | वैकल्पिक — cluster न हो तो steps स्थानीय प्रोसेस की तरह चलते हैं |

`brevis run`, `validate`, `marca`, `hash` और `version` को **कोई** डेटाबेस नहीं चाहिए।

## सोर्स से बिल्ड करना

यही वह रास्ता है जो संस्करण और commit छपी हुई बाइनरी बनाता है:

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

:::warning engine का अभी कोई tagged release नहीं है
रिपॉज़िटरी का हर tag SDK का है (`sdk/v*`); root module का कोई नहीं है।
`go install github.com/AreteAcademy/brevis/cmd/brevis@latest` branch के सबसे नए commit
पर resolve होता है और ऐसी बाइनरी बनाता है जो खुद को `brevis dev` बताती है, बिना commit
और बिना तारीख़ के — `-ldflags` सिर्फ़ `make build` और `docker build` से आते हैं।
ट्रेस करने लायक संस्करण के लिए image या `make build` इस्तेमाल करें।
:::

## Docker

एक ही बाइनरी की दो images, क्योंकि दोनों भूमिकाओं की ज़रूरतें उलटी हैं:

| tag | base | क्यों |
|---|---|---|
| `:0.11.2` | distroless | API सिर्फ़ HTTP परोसती है और कुछ चलाती नहीं — कोई shell नहीं, न्यूनतम सतह |
| `:0.11.2-worker` | alpine + tini | `run:` steps को shell चाहिए |

```bash
docker run --rm areteacademy/brevis:latest version
```

दोनों images [hub.docker.com/r/areteacademy/brevis](https://hub.docker.com/r/areteacademy/brevis)
पर हैं, `amd64` और `arm64` के लिए।

## पूरा स्थानीय वातावरण

रिपॉज़िटरी में एक `docker-compose.yml` है जो Postgres, API और शेड्यूलर खड़े कर देता है:

```bash
make up      # Postgres + API + scheduler
make smoke   # checks /health and /ready
make logs
make down
```

इंटरफ़ेस `http://localhost:8080` पर है।

## न्यूनतम कॉन्फ़िगरेशन

सिर्फ़ एक वेरिएबल ज़रूरी है:

```bash
export BREVIS_DATABASE_URL='postgres://brevis:brevis@localhost:5432/brevis?sslmode=disable'
```

इसके बिना, डेटाबेस इस्तेमाल करने वाले सबकमांड boot पर ही विफल हो जाते हैं — जानबूझकर।
जो प्रोसेस क्रेडेंशियल के बिना उठ जाती है, उसे समस्या पहले अनुरोध पर ही पता चलती है, और
तब तक readiness ऑर्केस्ट्रेटर से झूठ बोल चुकी होती है।

```
$ brevis migrate status
erro: BREVIS_DATABASE_URL e obrigatoria
```

पूरी सूची [कॉन्फ़िगरेशन](/docs/configuration/) में है।

## स्कीमा लागू करना

Migrations बाइनरी में एम्बेडेड हैं और अपने अलग सबकमांड से लागू होते हैं —
**`serve` स्कीमा कभी नहीं बदलता**:

```bash
brevis migrate up
brevis migrate status
```

## पुष्टि करना

```bash
brevis version
brevis validate examples/
```

```
  ok    daily_analytics              dag  5 steps, 5 dependencias  (manual)
  ok    daily-report                 chain  3 steps, 2 dependencias  cron 0 2 * * *
  ok    hello                        dag  4 steps, 4 dependencias  (manual)
```

अगर दोनों जवाब देते हैं, तो इंस्टॉल सही है। आगे [क्विकस्टार्ट](/docs/quickstart/) पर जाएँ।
