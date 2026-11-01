---
title: SQL
description: सादी .sql फ़ाइलें निर्भरता के क्रम में तालिकाएँ और view बन जाती हैं — workflow के step के रूप में या अकेले।
group: SQL में मॉडलिंग
order: 21
slug: sql
---

कोई templating नहीं: मॉडल एक ऐसी फ़ाइल है जो वैध SQL बनी रहती है, और क्रम खुद SQL को पढ़कर निकलता है। Postgres और BigQuery, एक ही conformance सुइट के तहत।

![SQL का प्रवाह: brevis-sql raw.orders पढ़ता है, जो SDK या Gateway की उतारी तालिका है। मॉडल staging.stg_orders उसके ऊपर view बनता है, और marts.orders उस view के ऊपर तालिका; क्रम SQL को पढ़ने से आता है। वही मॉडल Postgres या BigQuery पर बनते हैं।](/assets/flow-sql.svg)

## छह कमांड

| कमांड | क्या करती है |
|---|---|
| `brevis-sql compile` | हर मॉडल को parse करती है और हर edge resolve करती है, **कहीं कनेक्ट किए बिना** |
| `brevis-sql graph` | अनुमानित edges छापती है, ताकि गलत वाला दिखे, बाद में खोजना न पड़े |
| `brevis-sql build` | हर मॉडल को निर्भरता के क्रम में बनाती या बदलती है |
| `brevis-sql test` | हर मॉडल के टेस्ट चलाती है; हर एक ऐसा SELECT है जिसे कुछ नहीं मिलना चाहिए |
| `brevis-sql serve` | HTTP पर केवल-पढ़ने के लिए previews और queries का जवाब देती है, ताकि console को warehouse का क्रेडेंशियल न चाहिए |
| `brevis-sql generation bump` | दर्ज करती है कि तालिका दोबारा बनी, ताकि उस पर `brevis-sql-api` के cached जवाब पुराने पड़ जाएँ — `build --generations-from` यह हर उस मॉडल के लिए करता है जो वह बनाता है |

`--select orders+` इनमें से किसी को एक मॉडल और उसके नीचे के सब कुछ तक सीमित करता है।


## `brevis-sql serve` — क्रेडेंशियल के बिना warehouse पढ़ना

Console की Data और SQL स्क्रीन दिखाती हैं कि क्या उतरा और उसमें क्या है। इनमें से कोई warehouse तक नहीं पहुँचती: वे इस service से पूछती हैं, जो उनके बगल में चलती है और वह क्रेडेंशियल रखती है जो engine जानबूझकर नहीं रखता।

```bash
brevis-sql serve --connections brevis.yaml --addr 127.0.0.1:8088
```

सीमा बजट नहीं है: `--max-bytes` एक query को बाँधता है, और `--budget` प्रति कनेक्शन प्रति घंटे का योग बाँधता है — quote के विरुद्ध जाँचा जाता है, इसलिए इनकार पैसा खर्च होने से पहले आता है। यह सिर्फ़ उन warehouses को बाँधता है जो बाइट रिपोर्ट करते हैं, यानी BigQuery, Postgres नहीं; boot banner बताता है कि कौन-सा रुख लागू है, कोई न होना भी।

**सिर्फ़ वही पहुँच में है जो फ़ाइल घोषित करती है**, और डिफ़ॉल्ट रूप से इसका कुछ भी write नहीं है: हर स्टेटमेंट चलने से पहले वर्गीकृत होता है, जो read `--max-bytes` से ज़्यादा स्कैन करे उसका मूल्य लगाकर उसे कुछ खर्च होने से पहले मना कर दिया जाता है, और warehouse से खुद writes मना करने को कहा जाता है। ऑडिट पंक्ति में स्टेटमेंट का हैश होता है, स्टेटमेंट कभी नहीं।

**अब इसे अपने अलग service account की ज़रूरत नहीं।** `brevis-sql build` तालिकाएँ बनाता है, इसलिए उसकी भूमिका लिख सकती है, और जो service ऐसी किसी भी भूमिका से मना करती थी उसका मतलब बनाने, सँभालने और बदलने के लिए दूसरा खाता था। अब भूमिका ही रेखा थामे रखने वाली अकेली चीज़ नहीं है: Postgres हर स्टेटमेंट `READ ONLY` खोले गए लेनदेन के भीतर चलाता है — जहाँ `SELECT nextval('s')`, एक write जिसे कोई classifier भीतर से नहीं देख सकता, खुद Postgres से मना होकर लौटता है — और BigQuery उस dry run पर बताता है कि स्टेटमेंट किस प्रकार का है जो उसका मूल्य लगाता है, इसलिए `CREATE TABLE` या `EXPORT DATA` warehouse के अपने फ़ैसले पर मना होता है। जो warehouse इनमें से कुछ नहीं दे सकता, वह फिर भी ऐसे क्रेडेंशियल से मना करता है जो लिख सके।

`BREVIS_ENV=local` के बाहर token ज़रूरी है और service उसके बिना शुरू नहीं होगी।

### विकास का warehouse जिसे कोई बदल सकता है

```bash
BREVIS_SQL_SERVE_WRITES=warehouse_dev
```

यह **कनेक्शनों के नाम लेता है**। एक service कई warehouses तक पहुँचती है, और जिसके विरुद्ध आप विकास कर रहे हैं उसे खोलने से उसके बगल वाला नहीं खुलना चाहिए। नामित कनेक्शन पर हर परत पीछे हट जाती है और यह service ठीक उतनी ही शक्तिशाली है जितना उसका क्रेडेंशियल — पंक्तियों की सीमा स्क्रीन को बाँधती है और बाइट की सीमा पैसे को, और कोई भी यह नहीं बाँधती कि स्टेटमेंट क्या बदलता है।

जहाँ भी यह सेट हो वहाँ token ज़रूरी है, और `BREVIS_ENV=local` अपवाद नहीं है। Boot banner हर खुले कनेक्शन का नाम लेता है, console एडिटर के ऊपर banner बनाता है, और मेट्रिक्स पर `brevis_sql_writable_connection` दिखता है। **Production में इसे सेट न करें**; इस रिपॉज़िटरी का कोई manifest इसे सेट नहीं करता।

Tokens **नामित** हो सकते हैं: `BREVIS_SQL_SERVE_TOKEN_CONSOLE=…` `console` नाम का caller घोषित करता है, और उस bearer की बनाई हर ऑडिट पंक्ति में `"caller":"console"` होता है। पहचान क्रेडेंशियल है, किसी header में caller का भरा मान नहीं, इसलिए लॉग वह कुछ नहीं कह सकता जो उस token के धारक ने नहीं रखा था। मना किया गया token भी एक पंक्ति छोड़ता है, और जो प्रस्तुत किया गया था उसे कभी दर्ज नहीं करता।

## मॉडल एक फ़ाइल है जो वैध SQL बनी रहती है

कॉन्फ़िगरेशन शुरुआत का block comment है, इसलिए फ़ाइल highlighting के साथ एडिटर में खुलती है,
console में हाथ से चलती है, और `psql -f` उसे चलाता है।

```sql
/* brevis
materialized: table
tests:
  - not_null: [order_id]
  - unique: [order_id]
  - relationships: {column: customer_id, to: staging.stg_customers, field: customer_id}
*/
select * from staging.stg_orders
```

**कोई templating नहीं।** `models/<schema>/<name>.sql` का मतलब `<schema>.<name>` है, और निर्भरताएँ SQL पढ़कर आती हैं, किसी फ़ंक्शन कॉल से नहीं जो आपको लिखनी पड़े। `brevis-sql graph` छापता है कि क्या अनुमानित हुआ; हेडर में `depends_on:` दुर्लभ चूक के लिए स्पष्ट override है।

## compile से test तक

दो मॉडल: एक view जो `raw.orders` को साफ़ करता है, और एक तालिका जो उस पर निर्भर है।

`models/staging/stg_orders.sql`:

```sql
/* brevis
materialized: view
*/
select order_id, customer_id, amount, ordered_at
from raw.orders
where amount is not null
```

`models/marts/orders.sql`:

```sql
/* brevis
materialized: table
tests:
  - not_null: [customer_id]
  - unique: [customer_id]
*/
select customer_id, count(*) as orders, sum(amount) as revenue
from staging.stg_orders
group by customer_id
```

`compile` और `graph` कहीं कनेक्ट नहीं करते:

```bash
docker run --rm -v ./project:/project:ro areteacademy/brevis-sql:0.6.0 \
  compile --project /project
```

```text
2 models, 2 to build, 0 function(s)
  staging.stg_orders                 view  0 test(s)
  marts.orders                       table 2 test(s)
```

```bash
docker run --rm -v ./project:/project:ro areteacademy/brevis-sql:0.6.0 \
  graph --project /project
```

```text
staging.stg_orders
  └─ raw.orders (source)
marts.orders
  ├─ staging.stg_orders
```

Postgres के विरुद्ध `build` और `test`:

```bash
docker run --rm -v ./project:/project:ro \
  -e BREVIS_SQL_DSN=postgres://user:pass@host:5432/db areteacademy/brevis-sql:0.6.0 \
  build --project /project --dsn-from BREVIS_SQL_DSN
```

```text
  staging.stg_orders                 view  new   4ms
@brevis:{"type":"landed","target":"postgres://postgres/staging/stg_orders"}
  marts.orders                       table new   4ms
@brevis:{"type":"landed","target":"postgres://postgres/marts/orders","rows":2}
2 models built on postgres
```

```bash
docker run --rm -v ./project:/project:ro \
  -e BREVIS_SQL_DSN=postgres://user:pass@host:5432/db areteacademy/brevis-sql:0.6.0 \
  test --project /project --dsn-from BREVIS_SQL_DSN
```

```text
2 tests passed on postgres
```

प्रकाशित image से लिया गया असली आउटपुट — compile और graph कहीं कनेक्ट नहीं करते। Postgres के विरुद्ध, build और test "2 models built" और "2 tests passed" पर खत्म होते हैं।

### जब कोई टेस्ट विफल हो

ऐसे ऑर्डर के साथ जिसका `raw.orders` में कोई ग्राहक नहीं है, `build` पास हो जाता है और `not_null` टेस्ट नहीं। विफलता मॉडल, कॉलम, कितनी पंक्तियाँ और **query** का नाम बताती है — वही चीज़ जिसे आप console में चिपकाकर सँकरा करते जाते हैं:

```text
FAIL  marts.orders                   not_null         customer_id          1 row(s)
      SELECT COUNT(*) FROM (SELECT customer_id FROM marts.orders WHERE customer_id IS NULL) AS v
brevis-sql: 1 of 2 tests failed
```

प्रोसेस कोड 1 के साथ निकलती है, और workflow का step उसके साथ विफल होता है।

## इन्क्रीमेंटल मॉडल और उसका lookback

`materialized: incremental` को `unique_key` और `watermark` चाहिए। हर build केवल उन
पंक्तियों को पढ़ता है जो टारगेट में पहले से मौजूद सबसे बड़े watermark से आगे हैं, और
उन्हें कुंजी के आधार पर MERGE करता है। यह फ़िल्टर सख़्त है, इसलिए जो पंक्ति watermark के
बराबर है पर build के बाद कमिट होती है, या पुराने टाइमस्टैम्प के साथ देर से पहुँचती है, वह
`--full-refresh` तक दिखाई नहीं देती। वैकल्पिक `lookback` इसे ठीक करता है:

```sql
/* brevis
materialized: incremental
unique_key: [order_id]
watermark: updated_at
lookback: 2h
*/
```

मान एक पूर्ण संख्या और उसके बाद `s`, `m`, `h` या `d` होता है; इसके अलावा कुछ भी
मॉडल के नाम के साथ अस्वीकार कर दिया जाता है। फ़िल्टर `updated_at >= max - 2h` बन जाता है,
और MERGE दोहराई गई पंक्तियों को समा लेता है। lookback सेट होने पर watermark TIMESTAMP
कॉलम होना चाहिए। `lookback` के बिना SQL अपरिवर्तित रहता है।

## टेस्ट ऐसे SELECT हैं जो उल्लंघन करती पंक्तियाँ लौटाते हैं

`not_null`, `unique`, `accepted_values`, `relationships`। शून्य पंक्तियाँ पास हैं, और व्याख्या करने को और कुछ नहीं।

## यह बताता है कि इसने क्या लिखा

हर materialized मॉडल वह `@brevis:` पंक्ति छापता है जिसे Brevis engine पहले से पढ़ता है, इसलिए वह `/data` पर बाकी सब के बगल में दिखता है (ऊपर `build` का आउटपुट देखें)। View पंक्तियों की गिनती शून्य के बजाय रिपोर्ट ही नहीं करता: उसमें कोई नहीं होती, और वहाँ का शून्य उस तालिका से अलग नहीं पहचाना जा सकता जो सचमुच खाली है।

## दो warehouses, एक सुइट

PostgreSQL और BigQuery (`--dialect bigquery`), और दोनों टेस्ट का वही सुइट चलाते हैं — जो तब लिखा गया जब सिर्फ़ एक कार्यान्वयन था, ताकि दूसरा चुपचाप अलग न हो सके।

BigQuery Application Default Credentials से authenticate करता है: workload identity पर, वह pod की अपनी पहचान है, और `--dsn-from` उस वेरिएबल का नाम बताता है जिसमें project id है।

## 13 MB

Distroless, nonroot, बिना shell। आधिकारिक BigQuery क्लाइंट खाली बाइनरी में 31 MB और 526 पैकेज है, इसलिए यह BigQuery से उसके REST endpoint पर बात करता है — करीब 200 पंक्तियाँ, हर उस तरीके के लिए एक टेस्ट के साथ जिससे वह चुपचाप गलत हो सकता है। रिपॉज़िटरी का एक गेट उस क्लाइंट को नाम लेकर मना करता है, ताकि फ़ैसला नियमित dependency bump में बचा रहे।

## dbt के विरुद्ध सत्यापित

jaffle-shop, इस image से उसी seed डेटा पर बना जो dbt ने इस्तेमाल किया:

|  | नतीजा |
|---|---|
| प्रति मॉडल हर पंक्ति की पंक्तियाँ और md5 | **समान, 13 में से 13** |
| प्रति मॉडल कॉलम और टाइप | **समान, 13 में से 13** |
| 13 मॉडल, पहला build | 1.56 s |

## यह क्या नहीं करता

**dbt संगतता नहीं**, और यह कमी नहीं बल्कि फ़ैसला है। असली dbt प्रोजेक्ट के करीब एक तिहाई मॉडल `ref`, `source` और `config` से आगे कुछ इस्तेमाल नहीं करते; बाकी macros इस्तेमाल करते हैं, और packages डिज़ाइन से ही templating हैं। आधी संगतता जिसका कोई अनुमान न लगा सके, किसी न होने से बुरी है।

## आगे कहाँ जाएँ

| अगर आप चाहते हैं | यहाँ जाएँ |
|---|---|
| देखना कि चार हिस्सों में SQL कहाँ बैठता है | [इकोसिस्टम](/docs/ecosystem/) |
| brevis-sql को शेड्यूल्ड step के रूप में चलाना | [Core](/docs/core/) |
| वह डेटा उतारना जिसे यह मॉडल करेगा | [Go SDK](/docs/sdk/) या [Ingestion](/docs/ingestion/) |
