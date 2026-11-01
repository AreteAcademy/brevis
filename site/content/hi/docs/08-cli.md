---
title: CLI
description: Engine के बारह सबकमांड, flags, आर्ग्युमेंट और हर एक की ज़रूरतों के साथ।
group: संदर्भ
order: 8
slug: cli
---

Engine एक बाइनरी है जिसमें सबकमांड हैं, कई बाइनरी नहीं: API, शेड्यूलर और workers एक ही सिस्टम की भूमिकाएँ हैं,
और एक बाइनरी का मतलब एक संस्करण, एक image और एक बिल्ड रास्ता।

```
brevis [command] [flags]
```

| कमांड | डेटाबेस | `local` के बाहर क्रेडेंशियल | भूमिका |
|---|---|---|---|
| [`serve`](#serve) | **हाँ** | **ज़रूरी** | API + इंटरफ़ेस |
| [`scheduler`](#scheduler) | **हाँ** | **ज़रूरी** | दोनों लूप |
| [`migrate`](#migrate) | **हाँ** | **ज़रूरी** | स्कीमा |
| [`publish`](#publish) | **हाँ** | **ज़रूरी** | workflow और शेड्यूल सहेजता है |
| [`backfill`](#backfill) | **हाँ** | **ज़रूरी** | किसी दायरे को दोबारा प्रोसेस करता है |
| [`cancel`](#cancel) | **हाँ** | **ज़रूरी** | run रोकता है |
| [`run`](#run) | नहीं | — | अभी चलाता है, उसी इंस्टेंस पर |
| [`validate`](#validate) | नहीं | — | workflow YAML को validate करता है |
| [`brand`](#brand) | नहीं | — | ब्रांडिंग YAML को validate करता है |
| [`hash`](#hash) | नहीं | — | पासवर्ड हैश बनाता है |
| [`version`](#version) | नहीं | — | संस्करण, commit, बिल्ड |

पहले छह कॉन्फ़िगरेशन लोड करते हैं, जो `BREVIS_DATABASE_URL` के बिना **boot पर विफल** होता है। बाकी पाँच नहीं करते —
इसीलिए `validate` CI में काम करता है, जहाँ Postgres नहीं होता।

वही छह क्रेडेंशियल का नियम भी विरासत में लेते हैं: `BREVIS_ENV` के `local` से अलग होने पर, तीनों authentication
वेरिएबल के बिना उठना **boot त्रुटि** है।

---

## serve

HTTP API और इंटरफ़ेस को उठाता है। कोई flag नहीं — सब कुछ environment से आता है।

```bash
brevis serve
```

`SIGINT`/`SIGTERM` पर शालीनता से बंद होता है, ताकि deploy चल रहे अनुरोधों को न काटे। यह प्रोसेस **schedules नहीं बनाती**:
उसके लिए साथ में `brevis scheduler` चाहिए।

`api` image का `CMD` (distroless, बिना shell)।

---

## scheduler

दोनों लूप: शेड्यूलर schedules को run में बदलता है, dispatcher कतार से खींचकर चलाता है।

```bash
brevis scheduler --interval 5s --concurrency 4 --max-pods 10
```

| flag | प्रकार | डिफ़ॉल्ट | |
|---|---|---|---|
| `--interval` | duration | `10s` | चक्रों के बीच का अंतराल |
| `--concurrency` | int | `5` | एक साथ चलने वाले **run** |
| `--max-pods` | int | `5` | कुल मिलाकर एक साथ चलने वाले **steps** |

`worker` image का `CMD` (shell के साथ alpine, क्योंकि `run:` steps को shell चाहिए)।

---

## alert

शेड्यूलर ने जो अलर्ट दर्ज किए उन्हें डिलीवर करता है। `serve` और `scheduler` के बगल में तीसरी प्रोसेस।

```bash
brevis alert --interval 1s --max-attempts 6
```

| flag | प्रकार | डिफ़ॉल्ट | |
|---|---|---|---|
| `--interval` | duration | `1s` | डिलीवरी के चक्रों के बीच का अंतराल |
| `--max-attempts` | int | `6` | अलर्ट को undelivered दर्ज करने से पहले कोशिशें |

**यह इसलिए है कि अलर्ट कहाँ लिखा जाता है, इसलिए नहीं कि डिलीवरी को प्रोसेस चाहिए।** शेड्यूलर अलर्ट को उसी लेनदेन
में दर्ज करता है जिसमें वह विफलता जो उसे उचित ठहराती है — या तो run के प्रयास खत्म हुए और अलर्ट मौजूद है, या दोनों में से कुछ
नहीं हुआ। फिर किसी को उस तालिका को खाली करना है, और उसे Slack की गड़बड़ी और अपने ही रीस्टार्ट दोनों से बचना है। इससे पहले,
विफल होते webhook का मतलब एक लॉग पंक्ति था और ऐसा अलर्ट जो बस गायब हो गया।

कोई चैनल कॉन्फ़िगर न हो (`BREVIS_SLACK_WEBHOOK`) तो यह शुरू होने से मना करता है। जिस डिलीवरी प्रोसेस के पास डिलीवर करने की जगह
न हो, वह outbox को उतनी ही तेज़ी से "undelivered" में खाली कर देती है जितनी तेज़ी से वह भरता है, और वे पंक्तियाँ ठीक वैसी दिखती हैं
जैसे Slack ने उन्हें ठुकराया हो।

जिस अलर्ट को वह छोड़ देता है वह **सहेजा** जाता है: "उठाया गया, डिलीवर नहीं हुआ, 4 प्रयास, Slack से 403" वही पंक्ति है जो मायने रखती है,
क्योंकि यही वह मामला है जहाँ कोई ऐसे संदेश का इंतज़ार कर रहा है जो आ नहीं रहा।

`worker` image का `CMD`।

---

## report

आवधिक सार भेजता है कि क्या चला।

```bash
brevis report --window 168h              # sends it
brevis report --window 168h --dry-run    # prints it, sends nothing
```

| flag | प्रकार | डिफ़ॉल्ट | |
|---|---|---|---|
| `--window` | duration | `168h` | कितना पीछे तक देखना है |
| `--dry-run` | bool | `false` | भेजने के बजाय छापें |

यह **कमांड** है, लूप नहीं, और रिपोर्ट और अलर्ट में यही फ़र्क है: यह CronJob से चलती है, इसलिए कोई नया लूप, कोई नई स्थिति और कोई leader
election नहीं — cluster के पास पहले से शेड्यूलर है। `--dry-run` से कोई साप्ताहिक संदेश पर भरोसा करना सीखता है, यह पढ़कर कि वह क्या कहता।

यह अलर्ट outbox से **नहीं** गुज़रती। दोनों सिर्फ़ डिलीवरी चैनल साझा करते हैं, और कुछ नहीं: अलर्ट खोना ऐसा आउटेज है जिसे कोई
नहीं सुनता, और एक हफ़्ते का सार छूटना अगले हफ़्ते का सार है।

संदेश में runs, सफलताएँ, विफलताएँ, सबसे ज़्यादा विफल होने वाली pipelines, सबसे ज़्यादा समय लेने वाली, और SDK ने जो पंक्तियाँ और बाइट
रिपोर्ट किए वे होते हैं। उसमें CPU या मेमोरी **नहीं** होती, और उनके लिए शून्य भी नहीं छपता — engine उन्हें इकट्ठा नहीं करता, और संदेश बताता है
कि वे कहाँ मिलते हैं।

खाली विंडो यह कहती है, 100% रिपोर्ट नहीं करती। शून्य में से निकली सफलता दर वह संख्या है जो रिपोर्ट छाप सकती सबसे आश्वस्त करने वाली और
सबसे कम सच्ची: खाली हफ़्ते का आम तौर पर मतलब होता है कि शेड्यूलर बंद था।

`deployments/kubernetes/report.yaml` इसे साप्ताहिक चलाती है।

---

## migrate

```bash
brevis migrate up      # applies what is missing
brevis migrate down    # reverts the last one
brevis migrate status  # shows the state
```

ठीक एक आर्ग्युमेंट लेता है। Migrations बाइनरी में एम्बेडेड हैं और अपने सबकमांड से लागू होते हैं —
**`serve` स्कीमा कभी नहीं बदलता**।

---

## validate

एक या अधिक workflow फ़ाइलें validate करता है। फ़ाइल **या डायरेक्टरी** लेता है।

```bash
brevis validate examples/
```

```
  ok    daily_analytics              dag  5 steps, 5 dependencias  (manual)
  ok    hello                        dag  4 steps, 4 dependencias  (manual)
```

यह कोई डेटाबेस नहीं छूता और कोई सर्वर नहीं शुरू करता, इसलिए एडिटर में और CI में चलता है। यह गैर-शून्य के साथ निकलता है और विफलताएँ गिनता है।

---

## run

Workflow को **अभी, उसी इंस्टेंस पर** चलाता है: कोई कतार नहीं, कोई डेटाबेस नहीं, कोई शेड्यूलर नहीं।

```bash
brevis run examples/hello.yaml
brevis run wf.yaml --param load_full=true --retries 3 --timeout 5m
```

| flag | प्रकार | डिफ़ॉल्ट | |
|---|---|---|---|
| `--param` | दोहराने योग्य | — | घोषित पैरामीटर के लिए `key=value` |
| `--workdir` | string | फ़ाइल की डायरेक्टरी | steps की कार्य डायरेक्टरी |
| `--retries` | int | `1` | प्रति step प्रयास (`1` = कोई retry नहीं) |
| `--timeout` | duration | `0` | प्रति step timeout (`0` = कोई सीमा नहीं) |

`=` के बिना `--param` **त्रुटि** है, चेतावनी नहीं: `--param load_full`, मान भूलकर, डिफ़ॉल्ट के साथ चलता — और ऑपरेटर मान लेता कि मान लागू
हुआ।

:::warning चार सीमाएँ
`run` सिर्फ़ `BREVIS_ENV=local` के साथ काम करता है; `image:` वाला step उसी इंस्टेंस पर चलता है और यह बता देता है; Go task registry नहीं है,
इसलिए अपंजीकृत task के लिए `action:` उपलब्ध वालों के नाम बताते हुए विफल होता है; और `run` `BREVIS_HOSTS` नहीं पढ़ता, इसलिए `host:` वाला step यहाँ
चल ही नहीं सकता — वेरिएबल सेट होने पर भी वह host का नाम बताते हुए विफल होता है।
:::

---

## publish

Workflows और उनके schedules को डेटाबेस में सहेजता है। फ़ाइल या डायरेक्टरी लेता है।

```bash
brevis publish examples/hello.yaml
brevis publish workflows/ --project acme --prune
```

| flag | प्रकार | डिफ़ॉल्ट | |
|---|---|---|---|
| `--project` | string | `default` | प्रोजेक्ट slug |
| `--prune` | bool | `false` | सूची में अनुपस्थित workflows को प्रोजेक्ट से हटाता है |

`--prune` के साथ, हटाए गए workflows का इतिहास सुरक्षित रहता है।

---

## backfill

पहले से प्रकाशित workflow के बीते slot बनाता है। यह **कतार में डालता है, चलाता नहीं।**

```bash
brevis backfill daily --from 2026-01-01 --to 2026-01-31 --param load_full=true
```

| flag | | |
|---|---|---|
| `--from` | **ज़रूरी** | शुरुआती तारीख़, `YYYY-MM-DD` |
| `--to` | **ज़रूरी** | अंतिम तारीख़, `YYYY-MM-DD` |
| `--param` | | दायरे के **हर** slot पर लागू होता है |

`--to` पूरे दिन को शामिल करता है: भीतर से अंत उस तारीख़ का `23:59:59` बन जाता है।

---

## cancel

ऐसे run को रद्द करता है जो अभी प्रतीक्षा में है या चल रहा है। events फ़ीड में रद्द करने वाले के रूप में `cli` दिखता है।

```bash
brevis cancel 3f1c9a52-6d0e-4b7a-9c41-2b8e5d7f0a13
```

| जवाब | क्या हुआ |
|---|---|
| `canceled` | run कतार में प्रतीक्षा में था; वह उससे निकल गया और नहीं चलेगा |
| `cancel requested` | run चल रहा है; उसका dispatcher `--cancel-poll` (2s) के भीतर steps रोक देता है |
| `already ended: <status>` | run पहले ही खत्म हो चुका था; कुछ नहीं बदलता |

रद्द किए गए run को retry नहीं किया जाता। मौजूद न होने वाली id त्रुटि है।

---

## gateway

कोई gateway `/data` पर कैसे दिखता है: उसका कॉन्फ़िगरेशन, सोची-समझी क्रिया के रूप में प्रकाशित — उसका ट्रैफ़िक कभी नहीं, और runtime पर कभी नहीं।

```bash
gateway describe gateway.yaml > manifest.json   # the gateway's own binary
brevis gateway publish manifest.json
brevis gateway unpublish events_gateway
```

`publish` कुछ भी लिखने से पहले पूरे manifest को जाँचता है, पहली समस्या के stream और भूमिका का नाम बताते हुए, और उस gateway के गंतव्यों को एक
लेनदेन में बदल देता है। `unpublish` सेवामुक्त gateway को हटाता है, और उस नाम का कोई gateway प्रकाशित न होने पर त्रुटि है।

कोई `brevis-sql-api` Data → Endpoints पर कैसे दिखता है: हर endpoint क्या लेता और पढ़ता है, सोची-समझी क्रिया के रूप में प्रकाशित — उसका ट्रैफ़िक कभी नहीं।

```bash
brevis-sql-api describe --config api.yaml --out manifest.json   # the API's own binary
brevis endpoints publish manifest.json
brevis endpoints unpublish metrics-api
```

`publish` कुछ भी लिखने से पहले पूरे manifest को जाँचता है और उस API के endpoints को एक लेनदेन में बदल देता है; क्रेडेंशियल जैसा दिखने वाला नाम, डिफ़ॉल्ट या
टिप्पणी पूरे को ही मना करवा देती है।

यह `brevis publish` का मोड नहीं बल्कि अपनी कमांड है, क्योंकि workflows और gateways की सेट-अर्थ-व्यवस्था अलग है: `publish --prune` कभी किसी gateway की
पंक्तियों तक नहीं पहुँचना चाहिए।

---

## prune

Retention नीति लागू करता है। Brevis में यही **अकेली** चीज़ है जो कुछ भी हटाती है, और यह कभी अपने आप नहीं चलती — ऑपरेटर इसे शेड्यूल करता है।

```bash
brevis prune --dry-run          # what would go, changing nothing
brevis prune
brevis prune --purge-after 0    # trim only, never delete a run
```

| flag | डिफ़ॉल्ट | |
|---|---|---|
| `--trim-after` | `720h` | लॉग, चरणों और प्रकाशित आउटपुट को खाली करता है |
| `--purge-after` | `8760h` | run को पूरी तरह हटाता है; `0` कभी नहीं हटाता |

---

## brand

सर्वर शुरू किए बिना ब्रांडिंग फ़ाइल को validate करता है।

```bash
brevis brand brand.yaml
```

```
  ok    Brevis · Orchestration
        logo      /assets/logo.svg  (built-in symbol)
        accent    (the product's own)
        Powered by Brevis
```

Boot से एक व्यवहारगत फ़र्क: **फ़ाइल का न होना त्रुटि है**। `serve` में, अनुपस्थिति का मतलब "डिफ़ॉल्ट पहचान इस्तेमाल करो" है; जिसने path को validate
करने को कहा, वह यह सुनने की उम्मीद करता है कि वह मौजूद नहीं है।

---

## hash

`BREVIS_AUTH_PASSWORD_HASH` का मान बनाता है।

```bash
brevis hash
```

पासवर्ड टर्मिनल से **बिना echo** पढ़ा जाता है, आर्ग्युमेंट से कभी नहीं: आर्ग्युमेंट किसी भी प्रोसेस के `ps` में दिखता है और shell के इतिहास में रह जाता है।
यह 12 अक्षरों से छोटे पासवर्ड से मना करता है।

हैश अकेले **stdout** पर जाता है और लेबल **stderr** पर, इसलिए `brevis hash > hash.txt` सिर्फ़ वही सहेजता है जो मायने रखता है।

---

## version

```bash
brevis version
```

```
brevis 0.11.2
  commit  bb832ff
  build   2026-09-05T12:00:00Z
  go      go1.25.7 darwin/arm64
```

सीधे `go build` से बनी हो तो यह बिना commit और बिना तारीख़ के `brevis dev` छापती है — और जब कोई अजीब व्यवहार रिपोर्ट करे तो इसे release आर्टिफ़ैक्ट से
अलग पहचानना मायने रखता है।
