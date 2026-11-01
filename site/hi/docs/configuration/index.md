# कॉन्फ़िगरेशन

> हर environment वेरिएबल, डिफ़ॉल्ट, फ़ॉर्मेट और उसके न होने पर क्या होता है, के साथ।

*https://brevis.sh/hi/docs/configuration/ · brevis.sh docs (hi)*

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
| `BREVIS_ENV` | `local` | `local` लॉग टेक्स्ट में करता है और इंटरफ़ेस खुला छोड़ता है; बाकी सब को क्रेडेंशियल चाहिए और लॉग JSON में होते हैं |
| `BREVIS_HTTP_ADDR` | `:8080` | सुनने का पता |
| `BREVIS_LOG_LEVEL` | `info` | |
| `BREVIS_SHUTDOWN_TIMEOUT_SECONDS` | `15` | पूर्णांक; गैर-संख्यात्मक मान boot त्रुटि है |
| `BREVIS_BRAND_FILE` | `brand.yaml` | ब्रांडिंग; न हो = डिफ़ॉल्ट |
| `BREVIS_UI_URL` | — | अलर्ट में run के लिंक का आधार |
| `BREVIS_METRICS_ADDR` | — | Prometheus मेट्रिक्स का port; खाली = उजागर नहीं। देखें [ऑब्ज़र्वेबिलिटी](/hi/docs/observability/index.md) |
| `BREVIS_SLACK_WEBHOOK` | — | विफलता अलर्ट का गंतव्य। खाली = किसी को नहीं बताया जाता |

## Authentication

| वेरिएबल | |
|---|---|
| `BREVIS_AUTH_USER` | इंटरफ़ेस का उपयोगकर्ता |
| `BREVIS_AUTH_PASSWORD_HASH` | `pbkdf2-sha256$…` हैश, `brevis hash` से बना |
| `BREVIS_AUTH_SECRET` | session पर हस्ताक्षर के लिए 32+ बाइट (`openssl rand -base64 48`) |

तीनों साथ आते हैं, या कोई नहीं।

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
