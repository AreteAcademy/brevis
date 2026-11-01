# हर step के लिए एक pod

> हर step अपने pod में क्यों चलता है, और इससे लागत और अलगाव में क्या बदलता है।

*https://brevis.sh/hi/docs/pod-per-step/ · brevis.sh docs (hi)*

---

Kubernetes पर **workflow का हर step एक pod बन जाता है**, उस image के साथ जो उस step में
घोषित है। काम के इंतज़ार में बैठा कोई सामान्य worker नहीं है: काम अपना runtime साथ लाता है।

![डायग्राम: Core हर step के लिए एक pod शुरू करता है — extract Go पर, prepare Python पर, load Go पर और transform dbt पर। Steps एक-दूसरे को संदर्भ देते हैं, और उसे पढ़ने-लिखने का काम SDK करता है।](/assets/execution.svg)

```
scheduler                          cluster
─────────                          ───────
walks the graph
  for each ready step:
    builds the Pod  ───────────►  Pod: the step's image, the step's command
    follows status      ◄───────  Pending → Running → Succeeded/Failed
    follows the log     ◄───────  container stdout
    reads the exit code ◄───────  containerStatuses[0].terminated
    deletes the pod ───────────►  (gone)
```

## इससे क्या बदलता है

एकाश्म worker image को मजबूर करता है कि उसमें वह सब हो जो कोई भी step माँग सकता है।
व्यवहार में:

| | एक ही image | हर step के लिए pod |
|---|---|---|
| dbt step | 1.9 GB · 1Gi RAM | 1.9 GB · 1Gi RAM |
| उसके बगल में Go fetcher | **1.9 GB · 1Gi** | **12 MB · 32Mi** |
| dbt का संस्करण बढ़ाना | सब कुछ दोबारा बिल्ड | उस workflow की YAML में एक पंक्ति |
| मेमोरी रिसाने वाला step | worker और उसके पड़ोसियों को गिरा देता है | अकेला मरता है |

तीसरी पंक्ति सबसे कम स्पष्ट है और रोज़ के काम में सबसे महँगी: एक ही image के साथ, एक pipeline में dbt को
1.10 से 1.11 पर ले जाना सबमें यही बदलाव ज़रूरी कर देता है।

## भूमिका के हिसाब से images

Images प्रति **भूमिका** होती हैं, प्रति प्रोजेक्ट नहीं। image में बचा हर फ़ालतू हिस्सा उस step को चलाने
वाला हर node खींचता है:

| भूमिका | आकार | cold start |
|---|---|---|
| **Go** | **5.8 MB** | 0.18 s |
| **Python** | **118 MB** | 0.54 s |
| **dbt** | **620 MB** | 3.3 s |

पहले: सबके लिए एक ही 1.87 GB की image।

dbt के लिए, `dbt parse` बिल्ड के समय चलता है और `target/partial_parse.msgpack` image में चला जाता है —
हर invocation पर **प्रति pod 2.68 s कम**।

## हर step कहाँ चलता है

यह फ़ैसला boot पर एक बार होता है, और `BREVIS_PODS` पर तथा इस पर निर्भर करता है कि cluster है या नहीं:

| `BREVIS_PODS` | cluster के साथ | cluster के बिना |
|---|---|---|
| `auto` (डिफ़ॉल्ट) | `image:` वाला step pod बनता है | सब कुछ स्थानीय प्रोसेस की तरह, लॉग में चेतावनी के साथ |
| `on` | `image:` वाला step pod बनता है | **boot त्रुटि** |
| `off` | सब कुछ स्थानीय प्रोसेस की तरह | सब कुछ स्थानीय प्रोसेस की तरह |

**`host:` वाला step इनमें से किसी से नहीं गुज़रता।** उसे pod से पहले और प्रोसेस से पहले जाँचा जाता
है, और वह पहले से चल रही मशीन पर जाता है — उस पर `BREVIS_PODS` लागू नहीं होता, और वह कभी स्थानीय प्रोसेस
पर वापस नहीं आता। यह जानबूझकर है: `image:` चेतावनी के साथ स्थानीय execution पर इसलिए उतर आता है कि
container और प्रोसेस दो आवरणों में वही कमांड हैं, और host ऐसा नहीं है। वहीं लाइसेंस रहता है, या GPU, या डेटा।

`auto` डिफ़ॉल्ट इसलिए है कि वही बाइनरी दोनों जगह चलती है: लैपटॉप पर कोई service account माउंट नहीं होता और
वह स्थानीय प्रोसेस पर लौट आती है; cluster में होता है, और वह pods बनाने लगती है।

`on` उस deployment के लिए है जो **चुपचाप** स्थानीय execution **नहीं** बन सकता। वहाँ cluster का न होना boot
त्रुटि होना चाहिए।

:::warning `brevis run` कोई pod नहीं बनाता
`run` कमांड सिर्फ़ प्रोसेस executor बनाती है। `image:` वाला step उसी इंस्टेंस पर चलता है — और यह बता
देता है। चुप रहना ऐसा दिखाता कि step घोषित image पर चला, और इसी तरह की गलती तब सामने आती है जब नतीजा
पहले ही गलत हो चुका होता है।
:::

## पहचान और क्रेडेंशियल

Pods किस पहचान के साथ उठते हैं, यह **इंस्टॉलेशन** का फ़ैसला है, workflow का नहीं — pipeline को यह चुनने
में सक्षम नहीं होना चाहिए कि वह किस service account के रूप में चले। इसलिए यह environment से आता है:

```bash
BREVIS_POD_NAMESPACE=data
BREVIS_POD_SERVICE_ACCOUNT=brevis-runner
BREVIS_POD_PULL_SECRETS=registry-cred
BREVIS_POD_ENV_FROM_SECRETS=bigquery-cred,api-tokens
```

Pod मोड में task का environment cluster के Secrets से आता है — `BREVIS_TASK_ENV` से नहीं।

## Node selector और tolerations

```bash
BREVIS_POD_NODE_SELECTOR='kubernetes.io/arch=arm64'
BREVIS_POD_TOLERATIONS='dedicated=data:NoSchedule'
```

Tolerations में सिर्फ़ `Equal` ऑपरेटर स्वीकार है: `Exists` उस key वाले **किसी भी** taint को सह लेता, जो
environment वेरिएबल से आने वाले फ़ैसले के लिए बहुत व्यापक है।

गलत बनी toleration को boot त्रुटि बनाने के बजाय **अनदेखा** किया जाता है — वह pod को `Pending` में
छोड़ देती है, जो दिखाई देता है, जबकि boot से मना करना उन workflows को भी रोक देता जिन्हें उस pool की ज़रूरत
नहीं।

## विफलता को debug करना

```bash
BREVIS_POD_KEEP_ON_FAILURE=true
```

विफल pod को `kubectl logs` और `kubectl describe` के लिए रखे रखता है। Production में इसे बंद रखें:
रुके हुए pods cluster का quota खाते हैं।

## अगले कदम

- [Kubernetes](/hi/docs/kubernetes/index.md) — पूरा deployment
- [कॉन्फ़िगरेशन](/hi/docs/configuration/index.md) — हर `BREVIS_POD_*` वेरिएबल
- [ऑब्ज़र्वेबिलिटी](/hi/docs/observability/index.md) — दोनों प्रोसेस के मेट्रिक्स
