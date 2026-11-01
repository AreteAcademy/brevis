---
title: कॉन्फ़िगरेशन
description: हर environment वेरिएबल, डिफ़ॉल्ट, फ़ॉर्मेट और उसके न होने पर क्या होता है, के साथ।
group: संदर्भ
order: 10
slug: configuration
---

सारा कॉन्फ़िगरेशन **environment** से आता है, boot पर एक बार पढ़ा जाता है। उसके बाद कोई उसे नहीं देखता: जिस
प्रोसेस का कॉन्फ़िगरेशन बीच में बदलता है, उसका व्यवहार कोई दोहरा नहीं सकता।

## ज़रूरी

| वेरिएबल | |
|---|---|
| `BREVIS_DATABASE_URL` | Postgres। न होना = डेटाबेस इस्तेमाल करने वाले पाँच सबकमांड में **boot त्रुटि** |

## प्रोसेस

| वेरिएबल | डिफ़ॉल्ट | |
|---|---|---|
| `BREVIS_ENV` | `production` | `local` लॉग टेक्स्ट में करता है और इंटरफ़ेस खुला छोड़ता है; बाकी सब, सेट न होने पर भी, क्रेडेंशियल माँगता है और लॉग JSON में होते हैं। डेवलपमेंट के लिए `BREVIS_ENV=local` सेट करें |
| `BREVIS_HTTP_ADDR` | `:8080` | सुनने का पता |
| `BREVIS_LOG_LEVEL` | `info` | |
| `BREVIS_SHUTDOWN_TIMEOUT_SECONDS` | `15` | पूर्णांक; गैर-संख्यात्मक मान boot त्रुटि है |
| `BREVIS_DB_MAX_CONNS` | ड्राइवर डिफ़ॉल्ट, `max(4, CPUs)` | हर प्रोसेस के Postgres pool की सीमा; गैर-संख्यात्मक या ऋणात्मक मान boot त्रुटि है |
| `BREVIS_CONSOLE_STATEMENT_TIMEOUT_SECONDS` | `0` (बंद) | केवल कंसोल (`brevis serve`): Postgres उसके किसी भी ऐसे statement को रोक देता है जो इससे ज़्यादा देर चले, ताकि बेकाबू list query कोई कनेक्शन न थामे रखे। जहाँ pool startup पैरामीटर स्वीकार करता है वहाँ `30` अनुशंसित मान है; यह डिफ़ॉल्ट रूप से बंद है क्योंकि transaction मोड का PgBouncer अन्यथा कनेक्शन अस्वीकार कर देता है। इसे कनेक्शन के startup पैरामीटर के रूप में भेजा जाता है, इसलिए transaction मोड के PgBouncer को `ignore_startup_parameters = statement_timeout` चाहिए |
| `BREVIS_RETENTION_TRIM_DAYS` | `0` (बंद) | वैकल्पिक शेड्यूल्ड prune, जिसे `brevis scheduler` चलाता है: इस मान से पुराने runs का लॉग, फ़ेज़ और आउटपुट खाली करता है। प्रभाव `brevis prune --trim-after` जैसा ही है। `0` = scheduler कभी prune नहीं करता |
| `BREVIS_RETENTION_PURGE_DAYS` | `0` (कभी नहीं) | ऊपर वाले के साथ, इस मान से पुराने runs को डिलीट भी करता है। trim मान से कम नहीं होना चाहिए |
| `BREVIS_RETENTION_INTERVAL_HOURS` | `24` | शेड्यूल्ड prune कितनी बार चलता है (शुरू होने पर एक बार, फिर हर N घंटे) |
| `BREVIS_BRAND_FILE` | `brand.yaml` | ब्रांडिंग; न हो = डिफ़ॉल्ट |
| `BREVIS_UI_URL` | — | अलर्ट में run के लिंक का आधार |
| `BREVIS_METRICS_ADDR` | — | Prometheus मेट्रिक्स का port; खाली = उजागर नहीं। देखें [ऑब्ज़र्वेबिलिटी](/docs/observability/) |
| `BREVIS_SLACK_WEBHOOK` | — | विफलता अलर्ट का गंतव्य। खाली = किसी को नहीं बताया जाता |

## Authentication

| वेरिएबल | |
|---|---|
| `BREVIS_AUTH_USER` | इंटरफ़ेस का उपयोगकर्ता |
| `BREVIS_AUTH_PASSWORD_HASH` | `pbkdf2-sha256$…` हैश, `brevis hash` से बना |
| `BREVIS_AUTH_SECRET` | session पर हस्ताक्षर के लिए 32+ बाइट (`openssl rand -base64 48`) |

तीनों साथ आते हैं, या कोई नहीं।

| वेरिएबल | डिफ़ॉल्ट | |
|---|---|---|
| `BREVIS_AUTH_CLIENT_IP_HEADER` | — | वह हेडर जिसमें आपका proxy क्लाइंट का पता सेट करता है, जैसे `X-Forwarded-For`। **आख़िरी** एंट्री इस्तेमाल होती है (वह जो आपके proxy ने जोड़ी)। खाली = कनेक्शन का पता |

साइन-इन पर क्लाइंट पते के अनुसार थ्रॉटलिंग होती है (IPv6 उसके /64 से): तीन विफलताओं के बाद पता 2 सेकंड के लिए लॉक हो जाता है, और यह अवधि दोगुनी होती हुई 15 मिनट तक जाती है। **ingress या load balancer के पीछे, यह वेरिएबल सेट करें** (`api.ingress.enabled` चालू होने पर Helm chart यह करता है; `auth.clientIPHeader` से override करें): इसके बिना सभी क्लाइंट proxy का पता साझा करते हैं, इसलिए पासवर्ड का अंदाज़ा लगाने वाला एक व्यक्ति सबको लॉक कर देगा। ऐसी स्थिति में, `BREVIS_ENV=local` के बाहर, लॉकआउट 30 सेकंड पर सीमित रहता है और boot पर एक चेतावनी लॉग होती है। यह वेरिएबल तभी सेट करें जब हेडर किसी भरोसेमंद proxy द्वारा लिखा जाता हो और क्लाइंट उसे जाली न बना सके। हर पते पर एक समय में केवल एक पासवर्ड जाँच चलती है, इसलिए एक ही NAT के पीछे के दो लोग एक ही सेकंड में साइन इन करें तो उन्हें कुछ देर के लिए 429 दिख सकता है।

:::danger आधा कॉन्फ़िगर होना boot त्रुटि है
जिसने उपयोगकर्ता नाम भरा, वह मानता है कि दरवाज़ा बंद है। इंटरफ़ेस pipelines ट्रिगर करता है:
`POST /workflows/<slug>/trigger` ऐसा `dbt build` चलाता है जो warehouse में लिखता है। इंटरनेट पर खुला हो, तो वह
warehouse का रिमोट कंट्रोल है — और लॉग की चेतावनी उस मान्यता को नहीं मिटाती, क्योंकि जो प्रोसेस ठीक चल रही है उसका लॉग कोई नहीं पढ़ता।
:::

`BREVIS_ENV=local` के बाहर, क्रेडेंशियल के बिना उठना मना है:

```
error: BREVIS_ENV=production requires a credential: set BREVIS_AUTH_USER,
BREVIS_AUTH_PASSWORD_HASH (generate one with `brevis hash`) and BREVIS_AUTH_SECRET
```

:::note पुराने नाम अब भी काम करते हैं
चार वेरिएबल का नाम 2026-09-10 को बदला गया, जब प्रोजेक्ट के ऑपरेटर वाले हिस्से से आखिरी पुर्तगाली नाम गए:
`BREVIS_AUTH_USUARIO`, `BREVIS_AUTH_SENHA_HASH`, `BREVIS_AUTH_SEGREDO` और
`BREVIS_POD_MANTER_EM_FALHA`। जो इंस्टॉलेशन पुराना नाम सेट करता है वह काम करता रहता है और नए नाम का उल्लेख करती चेतावनी
लॉग करता है।

नया नाम जोड़ें, deploy करें, फिर पुराना हटाएँ — दोनों सेट हों तो नया नाम जीतता है। पुराने नाम एक major संस्करण में हट जाएँगे।

SDK में, `BREVIS_SDK_LIMITE_INLINE` उसी नियम के तहत `BREVIS_SDK_INLINE_LIMIT` बन गया।
:::

## Task का environment

| वेरिएबल | डिफ़ॉल्ट | |
|---|---|---|
| `BREVIS_TASK_ENV` | खाली | हर step को क्या मिलता है |

Task ऑर्केस्ट्रेटर का environment **विरासत में नहीं** लेता। कारण ठोस है: प्रोसेस के पास Postgres के उपयोगकर्ता नाम और पासवर्ड वाला
`BREVIS_DATABASE_URL` होता है, और workflow किसी और की लिखी मनमानी कमांड है — विरासत में देने का मतलब हर pipeline के हर step को डेटाबेस के
क्रेडेंशियल सौंपना होता।

Task को जो चाहिए, वह घोषित होता है:

```bash
BREVIS_TASK_ENV=GOOGLE_PROJECT_ID,STAGE   # forwards these two from the process
BREVIS_TASK_ENV=STAGE=prod                # sets a literal
BREVIS_TASK_ENV='*'                       # forwards everything EXCEPT BREVIS_*
```

`PATH` और `HOME` हमेशा शामिल रहते हैं — `PATH` के बिना कोई कमांड resolve नहीं होती, और त्रुटि ऐसा "not found" होती जो कुछ नहीं समझाता।

गायब नाम खाली string **नहीं** बनता: `GOOGLE_PROJECT_ID=""` dbt को गायब वेरिएबल से बुरे संदेश के साथ विफल करता।

## Kubernetes

ये **इंस्टॉलेशन** के फ़ैसले हैं, workflow के नहीं।

| वेरिएबल | डिफ़ॉल्ट | फ़ॉर्मेट |
|---|---|---|
| `BREVIS_PODS` | `auto` | `auto`, `on` या `off`; बाकी सब boot त्रुटि है |
| `BREVIS_POD_NAMESPACE` | cluster का | |
| `BREVIS_POD_SERVICE_ACCOUNT` | — | |
| `BREVIS_POD_PULL_SECRETS` | — | अल्पविराम से अलग की गई सूची |
| `BREVIS_POD_ENV_FROM_SECRETS` | — | अल्पविराम से अलग की गई सूची |
| `BREVIS_POD_ENV_FROM_CONFIGMAPS` | — | अल्पविराम से अलग की गई सूची |
| `BREVIS_POD_ALLOWED_SECRETS` | — | अल्पविराम से अलग की गई सूची |
| `BREVIS_POD_CREDENTIAL_PATH` | — | pod में माउंट किया गया path |
| `BREVIS_POD_CREDENTIAL_PVC` | — | क्रेडेंशियल रखने वाला PVC |
| `BREVIS_POD_NODE_SELECTOR` | — | `key=value,other=value` |
| `BREVIS_POD_TOLERATIONS` | — | `key=value:effect,…`; सिर्फ़ `Equal` ऑपरेटर |
| `BREVIS_POD_KEEP_ON_FAILURE` | `false` | `true` pod को जाँच के लिए रखता है |

सूचियों में खाली प्रविष्टियाँ अनदेखी की जाती हैं: `a,,b` टाइपो है, और खाली secret नाम पूरे pod को सर्वर से ठुकरवा देता।

## टेस्ट

| वेरिएबल | |
|---|---|
| `BREVIS_TEST_DATABASE_URL` | integration टेस्ट के लिए डेटाबेस; इसके बिना वे छोड़ दिए जाते हैं |

## Health endpoints

| | |
|---|---|
| `GET /health` | liveness — डेटाबेस से query **नहीं** करता |
| `GET /ready` | readiness — query करता है, और विफल हुई निर्भरता का नाम बताता है |

अलगाव जानबूझकर है: बाहरी service पर निर्भर liveness probe, डेटाबेस के डगमगाने पर Kubernetes से pod को load balancer से हटाने भर के बजाय **मरवा** देता है।

## एक पूरा उदाहरण

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
