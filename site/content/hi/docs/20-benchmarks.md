---
title: Benchmarks
description: हम gateway को कैसे मापते हैं, संख्याएँ क्या कहती हैं, और वे तीन गवाह जिनका सहमत होना ज़रूरी है।
group: Ingestion
order: 20
slug: benchmarks
---

एक कमांड, और वह पूरे gateway को मापती है:

```bash
BREVIS_BENCH_IMAGE=areteacademy/brevis-gateway:0.11.0 ./bench/run.sh
```

यह एक Postgres खड़ा करती है, gateway शुरू करती है, उस पर [k6] से भार डालती है, **बफ़र के खाली होने का इंतज़ार
करती है**, gateway का अपना `/metrics` scrape करती है और `bench/results/REPORT.md` लिखती है।

## तीन गवाह, और तीसरा वही है जो मायने रखता है

k6 जानता है कि producer ने क्या देखा। Gateway जानता है कि उसने क्या किया। सिर्फ़ पहले वाले के साथ चलाया गया load test तेज़
gateway को उससे अलग नहीं बता सकता जो `202` देता है और जो उसने स्वीकार किया उसे गिरा देता है।

इसलिए रिपोर्ट दोनों को आमने-सामने रखती है — और Postgres में पंक्तियाँ गिनती है:

| गवाह | events |
|---|---:|
| k6 को `accepted` बताया गया | 5,431,000 |
| gateway ने received गिना | 5,431,000 |
| Postgres में पंक्तियाँ | 5,431,000 |
| dead letter में रिकॉर्ड | 0 |

पहले दो दोनों gateway का अपना गणित हैं — एक उस response से जो उसने लिखा, एक उस counter से जो उसने बढ़ाया — और events
गँवाने वाला bug इन्हें **आपस में** सुसंगत रख सकता है। डेटाबेस अकेला है जिसने हमारी बात पर भरोसा नहीं किया।

**जिस run में तीनों असहमत हों वह विफल हुआ**, percentiles कितने भी अच्छे हों।

## ऊपर वाला run

`bench/results/REPORT.md` में 2026-09-26 का run:
`areteacademy/brevis-gateway:0.11.0`, Darwin arm64, 11 CPUs, उसी kernel पर container में Postgres 17,
`write: append` के साथ `auto_table[columns]`:

| | |
|---|---:|
| स्वीकार किए गए events | **5,431,000** (135,768/s) |
| भेजी गई body | 40s में 1,156.7 MiB |
| `503` (backpressure) | 0 |
| p50 / p95 / p99 | 5.9 ms / 16.0 ms / 22.7 ms |
| डिलीवर किए गए batches | 1,087, कोई दफ़न नहीं |

## Thresholds ही असली बात हैं

जब कोई विफल होता है तो k6 गैर-शून्य के साथ निकलता है, इसलिए यह **गेट** है, चार्ट नहीं:

| threshold | क्यों |
|---|---|
| `p95 < 500ms` | स्वीकार का रास्ता sink की देरी से दूर रहना चाहिए। `COPY` का इंतज़ार करता अनुरोध वही दोष है जिसके विरुद्ध async pipe है। |
| `http_req_failed < 1%` | `503` ठीक है; timeout या 5xx नहीं। |
| `events accepted > 1000` | threshold सुइट उस gateway के विरुद्ध भी पास हो जाता है जिसने कुछ जवाब ही नहीं दिया। |

ये जानबूझकर ढीले हैं: यह उसी मशीन पर चलता है जो CI ने दी। ये **परिमाण का एक क्रम** पकड़ते हैं, जैसा regression दिखता है,
एक प्रतिशत नहीं।

**`503` विफलता नहीं है।** यह gateway का ऐसे event को मना करना है जिसे रखने की जगह उसके पास नहीं है, और दोबारा कोशिश करना सुरक्षित है —
`ingestion_id` event का स्थिर फ़ंक्शन है, इसलिए वही body दोबारा भेजना वही रिकॉर्ड है। उसे त्रुटि गिनना backpressure
को bug जैसा दिखा देता।

## यह क्या नहीं मापता

- **BigQuery नहीं।** गंतव्य container में एक Postgres है। Warehouse का load-job quota, commit की देरी और DDL
  concurrency इन संख्याओं में नहीं हैं और हो भी नहीं सकते।
- **टिकाऊपन नहीं।** `durability: memory` इकलौता स्तर है, इसलिए `202` का मतलब *RAM में स्वीकार* है। यह मापता है कि वह कितनी
  तेज़ी से होता है, यह नहीं कि `SIGKILL` के बाद क्या बचता है।
- **आपका हार्डवेयर नहीं।** इसे एक आकार और अनुपात की तरह पढ़ें।

## प्रोफ़ाइल के हिसाब से स्थानीय क्लाउड

```bash
docker compose -f docker-compose.drivers.yml --profile gcp up -d
docker compose -f docker-compose.drivers.yml --profile all up -d
```

प्रोफ़ाइल वाली एक फ़ाइल, हर क्लाउड के लिए एक नहीं: दो फ़ाइलें दो प्रोजेक्ट नाम हैं, और दोनों में `postgres` एक बार
engine का development डेटाबेस बदल चुका है।

### floci BigQuery का उतना हिस्सा नहीं कवर करता जितना लगता है

मापा गया है, मान लिया नहीं गया:

| | |
|---|---|
| dataset और तालिका बनाना | ✅ |
| `timePartitioning` और `clustering` सुरक्षित | ✅ — `0.9.1` वाले bug का आकार |
| कॉलम का `PATCH` | ✅ — `auto_table` का विकास |
| `insertAll` | ✅ |
| queries | ✅, सिर्फ़ `/var/run/docker.sock` माउंट होने पर |
| **load jobs** | ❌ `Only QUERY jobs are supported by the floci BigQuery emulator` |

SDK BigQuery में **सिर्फ़** load jobs से लिखता है। इसलिए floci `auto_table` की हर वह पंक्ति परखता है जो payload को DDL में
बदलती है — और **Brevis का एक भी write नहीं**। आज उस आधे हिस्से का कोई स्थानीय टेस्ट नहीं है, और उसका होना मूल्यवान है।
लेकिन floci के विरुद्ध हरा सुइट यह नहीं कहता कि BigQuery काम करता है।

[k6]: https://k6.io
