---
title: Ingestion sinks
description: छह गंतव्य, हर डेटाबेस रिकॉर्ड कैसे लिखता है, और image सिर्फ़ वही क्यों ढोती है जो आप इस्तेमाल करते हैं।
group: Ingestion
order: 19
slug: ingestion-sinks
---

छह गंतव्य, और सूची जानबूझकर स्पष्ट है — जो connector *योजना में* है और जो *चलता* है, इसे चुनने वाले के लिए दो अलग चीज़ें हैं।

| sink | स्थिति | write क्या है | किसके विरुद्ध प्रमाणित |
|---|---|---|---|
| `pubsub` | **चलता है** | प्रति batch एक publish, attributes और ordering key event के अपने फ़ील्ड से | emulator |
| `postgres` | **चलता है** | `COPY FROM STDIN`, या एक लेनदेन में staged `INSERT` | असली Postgres |
| `mysql` | **चलता है** | लेनदेन में multi-row `INSERT`, या `INSERT IGNORE` | असली MySQL |
| `files` | **चलता है** | डायरेक्टरी, `gs://` या `s3://` में NDJSON ऑब्जेक्ट | फ़ाइलसिस्टम, और S3 |
| `bigquery` | **चलता है** | पंक्तियाँ लोड होती हैं, या staged होकर `ingestion_id` पर `MERGE` | **सिर्फ़ कॉन्फ़िग — कोई emulator नहीं है** |
| `redshift` | **चलता है** | batch S3 पर, फिर `COPY`, फिर `MERGE` | **सिर्फ़ कॉन्फ़िग — कोई emulator नहीं है** |

**छह में से दो end-to-end अप्रमाणित हैं**, और तालिका यह कहती है, "चलता है" शब्द को ऐसे दावे का वाहक बनने देने के बजाय जिसे किसी ने जाँचा नहीं। BigQuery और Redshift का कोई स्थानीय emulator नहीं है, इसलिए जो परखा जाता है वह कॉन्फ़िग, इनकार और नीचे का driver है — जिसे SDK का अपना सुइट पहले से कवर करता है। दोनों में से किसी का पहला production उपयोग असली प्रमाण है, और वह कम जोखिम वाला stream होना चाहिए।

अज्ञात `type:` **शुरू होते समय मना** होता है, यह बताते हुए कि *यह बाइनरी* क्या ढोती है। जो gateway ऐसे sink पर शुरू हो जाए जो उसके पास नहीं है, वह ऐसे कारण से events गिराता है जो किसी को दिखता नहीं।

`gs://` और `s3://` अलग connectors नहीं हैं: `files` तीनों path रूप पढ़ता है, इसलिए dead letter लैपटॉप पर फ़ोल्डर है और production में bucket, यहाँ कुछ बदले बिना।

## हर एक कैसे लिखता है

तालिका में लिखने वाले चारों गंतव्यों पर `write` **ज़रूरी है और कभी डिफ़ॉल्ट नहीं होता**, क्योंकि दोनों मोड सचमुच अलग हैं और केवल तालिका का मालिक जानता है कि कौन-सा है:

| | `append` | `merge` |
|---|---|---|
| **postgres** | `COPY FROM STDIN` | `INSERT … ON CONFLICT (ingestion_id) DO NOTHING` |
| **mysql** | प्रति block multi-row `INSERT` | `INSERT IGNORE` |
| **bigquery** | पंक्तियाँ लोड होती हैं (inline सीमा से ऊपर GCS के ज़रिए) | `MERGE … WHEN NOT MATCHED THEN INSERT` |
| **redshift** | S3 से `COPY` | staging तालिका में `COPY`, फिर `MERGE` |

`merge` का अर्थ **चारों में एक ही है**: idempotent insert, और **पहली डिलीवरी जीतती है**। दोबारा की डिलीवरी अनदेखी होती है, लागू नहीं — जो event के लिए सही है, जो एक बार हुआ और बदलता नहीं, और बदलती स्थिति ढोने वाली पंक्ति के लिए गलत।

Postgres और MySQL को `merge` के लिए `ingestion_id` पर **`UNIQUE` index चाहिए**, और उसके बिना चुपचाप append करने के बजाय मना करते हैं:

```sql
CREATE UNIQUE INDEX CONCURRENTLY ON landing.orders (ingestion_id);
```

Index के बिना `ON CONFLICT` के पास मिलाने को कुछ नहीं होता, और हर दोबारा की डिलीवरी उस तालिका में डुप्लिकेट बनाती जिसके मालिक ने उलटा माँगा था। इनकार खोया हुआ batch नहीं है: उसका retry होता है, फिर कारण और `CREATE INDEX` के साथ रिकॉर्ड पर dead letter में दफ़नाया जाता है — जब तक कोई index बनाता है, कोई events नहीं खोता।

**`upsert` नाम लेकर मना है।** `ON CONFLICT DO UPDATE`, जहाँ *आखिरी* डिलीवरी जीतती है, असली मोड है जो **लागू नहीं** है। शब्द को स्वीकार करके `merge` की तरह बरतना ठीक वही विफलता है जिसे रोकने के लिए वह फ़ील्ड है, इसलिए कॉन्फ़िग यही कहता है।

## हर एक, वह जो सिर्फ़ उसी के बारे में सच है

```yaml
# Postgres: schema-qualified name, and the table has to exist.
sink: {type: postgres, dsn_from: BREVIS_DSN, table: landing.clicks, write: merge}

# MySQL: no COPY, so throughput is an order below Postgres on the same
# hardware. Flush wider here.
sink: {type: mysql, dsn_from: BREVIS_DSN, table: landing.clicks, write: merge}

# BigQuery: no dsn_from. It authenticates with the pod's own credentials, which
# is what a workload identity is for. The name has no dots — the project and
# the dataset are their own fields.
sink: {type: bigquery, project: acme-prod, dataset: landing, table: clicks, write: merge}

# Redshift: two hops, not one. It is columnar, a row-by-row INSERT pays the cost
# of a block, and the only workable load is COPY from S3. So every batch becomes
# an object in the staging prefix and then a COPY — a stream flushing every
# second writes 86,400 objects a day. Flush much wider here.
sink:
  type: redshift
  dsn_from: BREVIS_RS_DSN
  table: landing.clicks
  staging: s3://acme-staging/gateway/
  iam_role: arn:aws:iam::123456789012:role/redshift-copy
  write: merge
```

`dsn_from` कनेक्शन string रखने वाले **environment वेरिएबल** का नाम लेता है, string का कभी नहीं: DSN पासवर्ड ढोता है और वह फ़ाइल git में है। यह वही बँटवारा है जो workflow में `secrets:` करता है।

`iam_role` role है और driver **key स्वीकार नहीं करेगा**: `COPY` के URL में key cluster के query log में पहुँच जाती है, जिसे बहुत लोग पढ़ते हैं।

## कोई कॉलम घोषणा नहीं

Gateway कॉलम सूची नहीं भेजता। Pipeline अपनी घोषित करती है क्योंकि उसकी पंक्तियों का एक आकार है जिसे वह नियंत्रित करती है; gateway का batch वह है जो *N* क्लाइंट ने एक flush विंडो में पोस्ट किया, और आकारों के भिन्न होने की अनुमति है।

Driver सूची को **तालिका से ही** निकालता है, batch जो ढोता है उससे काटकर, सर्वर को छूने से **पहले**। जो फ़ील्ड तालिका में नहीं है वह उस संदेश के साथ मना होता है जो उसे ठीक करता है, `COPY` के बीच में `column "x" of relation "y" does not exist` से विफल होने के बजाय; जो फ़ील्ड कोई event छोड़ दे वह `NULL` लिखा जाता है।

## Import सूची ही चयन है

Sinks hooks की तरह **compile होकर भीतर** होते हैं, और बाइनरी वही पंजीकृत करती है जो उसके पास है:

```go
func main() {
    sinks := gateway.NewSinks()
    sinks.MustRegister(postgres.Sink, postgres.New)
    sinks.MustRegister(files.Sink, files.New)
    gateway.Main(nil, gateway.WithSinks(sinks))
}
```

Go linker वह छाँट देता है जिसे कोई संदर्भित नहीं करता, इसलिए जो बाइनरी BigQuery driver कभी import नहीं करती वह BigQuery नहीं ढोती — न Arrow, न Storage Write API। `database/sql` हमेशा ऐसे ही काम करता आया है।

यह उतने का है जितना सुनने में लगता है:

| बिल्ड | पैकेज | बाइनरी |
|---|---|---|
| छहों sinks, दोनों ऑब्जेक्ट स्टोर, दोनों metastores | करीब 900 | 55.0 MB |
| `postgres` + स्थानीय `files` | करीब 240 | **10.2 MB** |

ऑब्जेक्ट स्टोर **दूसरी** registry हैं, क्योंकि स्कीम गंतव्य नहीं है: `files` एक sink है जो डायरेक्टरी, `gs://` और `s3://` में लिखता है, और जो बिल्ड सिर्फ़ स्थानीय रूप से लिखता है उसे ऐसा करने के लिए AWS SDK नहीं ढोना चाहिए।

### दो प्रकाशित images

| image | क्या ढोती है | pull (amd64) |
|---|---|---|
| `areteacademy/brevis-gateway:0.32.0` | छह sinks, S3, GCS, Redis, memcached | 19.1 MB |
| `areteacademy/brevis-gateway:0.32.0-slim` | `postgres`, स्थानीय `files`, `auto_table` | **5.2 MB** |

दोनों एक ट्री के एक बिल्ड से, इसलिए दोनों tags हमेशा वही commit हैं। `latest-slim` नहीं है — `latest` पहले से ऐसा tag है जिसे किसी को deploy नहीं करना चाहिए।

**कोई और जोड़ी चाहिए?** `cmd/gateway-slim` पंद्रह पंक्तियाँ है और import block ही उसका पूरा कॉन्फ़िगरेशन है। उसकी नकल करें, imports बदलें, बिल्ड करें। जो hook चाहता है वह पहले से अपनी बाइनरी बना रहा है, इसलिए sinks चुनने में उसे कुछ और नहीं लगता।

### इनकार बताता है कि आप कौन-सा बिल्ड थामे हैं

```
sink type "bigquery" is not one this binary carries (it has: files, postgres).
Sinks are compiled in, so this is a build that left it out rather than a
destination that does not exist.
```

स्थिर सूची उस व्यक्ति को कॉन्फ़िग की ऐसी गलती ढूँढने भेज देती जो उसने की नहीं। ऑब्जेक्ट स्टोर पर भी यही लागू होता है: S3 backend के बिना बाइनरी में `s3://` dead letter **शुरू होते समय**, स्कीम का नाम लेकर मना होता है, पहले batch पर नहीं जिसे उसे दफ़नाना पड़ता।

## एक route, N तालिकाएँ, कुछ घोषित नहीं

```yaml
sink:
  type: auto_table
  shape: columns
  naming: {pattern: '^(app|svc)_[a-z0-9_]{2,40}$', allow: [app_, svc_], max_new_per_hour: 20}
  metastore: {type: redis, addr_from: BREVIS_METASTORE_ADDR, ttl: 60s}
  into: {type: bigquery, project: acme-prod, dataset: landing, write: merge}
```

`auto_table` **route करता है, लिखता नहीं**: `into` वह है जो लिखता है, और उसमें कोई `table` नहीं — तालिका हर event से आती है। `table` वाला `into` शुरू होते समय मना होता है, क्योंकि router के नीचे की निश्चित तालिका चुपचाप जीतती और हर event उसी में उतरता।

### Envelope

Body एक **envelope** है: ऊपर नियंत्रण फ़ील्ड, रिकॉर्ड `data` के भीतर।

```json
{
  "table_name": "app_orders",
  "description": "App orders",
  "operation": "INSERT",
  "unique_key": "id",
  "data": {
    "id": "A-1",
    "total": 150,
    "customer": {"id": 7, "uf": "SP"},
    "items": [{"sku": "X", "qty": 2}]
  }
}
```

| फ़ील्ड | | यह क्या है |
|---|---|---|
| `table_name` | **ज़रूरी** | यह कहाँ उतरता है |
| `data` | **ज़रूरी** | रिकॉर्ड, और सिर्फ़ वही |
| `unique_key` | डिफ़ॉल्ट `id` | `data` का कौन-सा फ़ील्ड रिकॉर्ड की पहचान है — **`merge` के तहत ज़रूरी** |
| `operation` | डिफ़ॉल्ट `INSERT` | `INSERT`, `UPDATE` या `DELETE` |
| `description` | वैकल्पिक | **स्वीकार किया जाता है और अभी इस्तेमाल नहीं होता** — नीचे देखें |

`table_name` का `total` और `customer` के बगल में बैठना **transport** के फ़ील्ड का **रिकॉर्ड** का फ़ील्ड होने का दिखावा करना था। उन्हें अलग करना ही हर CDC फ़ॉर्मेट का आकार है — और यही उपसर्ग आरक्षित करना संभव बनाता है, क्योंकि अब एक ही जगह है जहाँ सिर्फ़ producer लिखता है।

### Key `merge` की है, envelope की नहीं

`unique_key` वैकल्पिक है, और वह **ज़रूरी** है या नहीं यह write मोड पर निर्भर करता है — क्योंकि दोनों मोड अलग चीज़ें चाहते हैं:

| | |
|---|---|
| `write: merge` | **ज़रूरी।** मोड प्रति रिकॉर्ड एक पंक्ति रखने के लिए है, और "प्रति रिकॉर्ड" का अर्थ `brevis_record_key` है: वर्तमान संस्करण निकालने वाला `qualify` उसी पर partition करता है। जिस merge तालिका में ऐसी पंक्तियाँ हों जो कोई रिकॉर्ड नहीं बतातीं, उसे कोई resolve नहीं कर सकता। |
| `write: append` | **वैकल्पिक।** Append तालिका लॉग है, और लॉग की प्रविष्टि का किसी रिकॉर्ड *के बारे में* होना ज़रूरी नहीं: ऑडिट पंक्ति, webhook, मेट्रिक नमूना। वहाँ `id` माँगना producers से उसे गढ़वाता, जो `NULL` से बुरा है क्योंकि असली दिखता है। |

`append` के तहत कॉलम nullable बनता है और बिना key वाली पंक्ति `NULL` ढोती है — `""` नहीं, जो अलग तथ्य है। और `brevis_ingestion_id` फिर भी मौजूद है: key के बिना, **सामग्री ही key है**, इसलिए वही दस्तावेज़ दो बार वही id है।

**`data` में न होने वाले फ़ील्ड का नाम लेना दोनों मोड में त्रुटि है।** "आपने कहा नहीं" और "आपने ऐसा कहा जो वहाँ नहीं है" अलग गलतियाँ हैं, और केवल पहली पास होती है:

```json
{"accepted":0,"rejected":["event 0: \"unique_key\" names \"pedido_id\" as this
 record's identity and \"data\".\"pedido_id\" is missing or empty. Drop
 \"unique_key\" to fall back to \"id\", or send the field"]}
```

**`description` पढ़ा जाता है और आज कहीं नहीं जाता।** यह console के ingestion पेज के लिए अनुबंध में है, जो अभी लिखा नहीं गया। यह यहाँ इसलिए कहा गया है कि चुपचाप स्वीकार किया गया फ़ील्ड वह है जिसके बारे में कोई मानता है कि वह सहेजा जा रहा है।

### आठ निश्चित कॉलम, और रिकॉर्ड जो भी ढोए

| कॉलम | | |
|---|---|---|
| `brevis_ingestion_id` | `STRING` | पहचान, और merge की key |
| `brevis_record_key` | `STRING` | `data[unique_key]` — यह किस **रिकॉर्ड** के बारे में है; `append` के तहत `NULL` |
| `brevis_operation` | `STRING` | `INSERT`, `UPDATE` या `DELETE` |
| `brevis_received_at` | `TIMESTAMP` | आगमन, **हमारी** घड़ी पर — partition कॉलम |
| `brevis_loaded_at` | `TIMESTAMP` | write, **गंतव्य** द्वारा मुहर लगाया गया |
| `brevis_stream` | `STRING` | किस route ने लिखा |
| `brevis_gateway` | `STRING` | कौन-सा deployment |
| `brevis_received_bytes` | `INT64` | event **कितना बड़ा आया**, envelope समेत |

ये आठ **SDK के** हैं, इस sink के नहीं: `sdk.LandingSchema` उन्हें घोषित करता है और `sdk.LandingID` पहचान गढ़ता है, इसलिए pipeline वही तालिका वही ids के साथ उतार सकती है। एक परिभाषा, क्योंकि दो भटक जातीं। देखें [SDK का लैंडिंग लेआउट](/docs/sdk/#लडग-लआउट)।

**`brevis_` आरक्षित है।** उस उपसर्ग वाली कोई भी key ढोने वाला `data` **प्रति event** मना होता है — वरना producer नियंत्रण फ़ील्ड गढ़ लेता, और गढ़ा हुआ `brevis_received_at` न होने से बुरा है क्योंकि असली दिखता है।

**`brevis_loaded_at` डेटाबेस का `DEFAULT` है**, gateway का भेजा मान नहीं। Gateway **dispatch** का समय जानता है, गंतव्य **write** का समय जानता है, और दोनों कॉलम का अंतर हर पंक्ति के लिए असली end-to-end देरी है, कुछ instrument किए बिना।

**`brevis_received_at` partition कॉलम है, क्लाइंट की घड़ी कभी नहीं**: producer की घड़ी गलत या अनुपस्थित हो सकती है, और क्लाइंट के नियंत्रण वाला partition कॉलम ऐसा क्लाइंट है जो 2035 में लिख सकता है। बिना partition वाली लैंडिंग तालिका ऐसा query बिल है जो सिर्फ़ बढ़ता है। Clustering `brevis_record_key` से है, क्योंकि ऐसी तालिका के साथ हर कोई एक रिकॉर्ड का इतिहास पढ़ता है।

एक घोषणा हर गंतव्य को परोसती है, क्योंकि SDK का DDL generator हर टाइप का अनुवाद करता है: `STRING` Postgres पर `TEXT`, MySQL पर `LONGTEXT` और BigQuery पर `STRING` बनता है; `JSON` क्रमशः `JSONB`, `JSON` और `JSON` बनता है।

### वॉल्यूम, वॉल्यूम मापने के बुनियादी ढाँचे के बिना

`brevis_received_bytes` **मुफ़्त** है: gateway `buffer.flush.size` चलाने के लिए ये बाइट decode पर पहले से गिनता है, और अब तक वह संख्या वहीं मर जाती थी। अकेले रिकॉर्ड को मापने में प्रति event एक `json.Marshal` लगता — parse पहले से जो 3.1 µs खर्च करता है उसके विरुद्ध 2.0 µs, **hot path पर 67% ज़्यादा** — ऐसी संख्या को सुधारने के लिए जिसका काम रुझान और आरोपण है।

यह उसका जवाब देता है जो मेट्रिक्स **संरचनात्मक रूप से नहीं दे सकते**। हर `brevis_gateway_*` series प्रति *stream* है और जानबूझकर उस पर `table` label नहीं है: `auto_table` के साथ एक route N तालिकाएँ बन जाता है और वह label producer के चुनने की चीज़ है। इसलिए "कौन-सी तालिका बढ़ रही है, और कब से" का जवाब कहीं नहीं था।

```sql
select date(brevis_received_at) day,
       count(*) events,
       sum(brevis_received_bytes)/1e9 gb
from app_orders
group by 1 order by 1 desc
```

कॉलम के बिना वही सवाल पूरे JSON कॉलम को स्कैन करना है — `sum(length(to_json_string(data)))` — करीब **पचास गुना ज़्यादा स्कैन किए गए बाइट**, हर बार जब कोई पूछे। कॉलम पहली query पर ही अपना खर्च निकाल लेता है।

**यह ingress है, storage नहीं।** गंतव्य पंक्ति को typed और compressed रखता है: JSON के 400 बाइट डिस्क पर 80 हो सकते हैं। इसका योग वह देता है जो *आया*, वह नहीं जिसे रखने का बिल लगता है।

और **producer इसे नहीं लिख सकता।** Gateway hook के बाद मान पर मुहर लगाता है और envelope में जो था उसे अधिलेखित करता है — जो क्लाइंट यह फ़ील्ड भेजता वह अपना वॉल्यूम खुद रिपोर्ट करता, और उसका योग कुछ मायने नहीं रखता।

### वॉल्यूम series opt-in हैं

```bash
BREVIS_INGESTION_METRICS=true
```

```
brevis_gateway_ingested_bytes_total{stream,table}
brevis_gateway_ingested_events_total{stream,table}
```

**डिफ़ॉल्ट रूप से बंद**, और डिफ़ॉल्ट ही मुद्दा है: `table` वह label है जो *producer* चुनता है। इस फ़ाइल में बाकी सब कुछ उस पर label होता है जो ऑपरेटर ने YAML में लिखा, इसलिए series की संख्या प्रोसेस शुरू होने से पहले ज्ञात है। अपग्रेड करने की वजह से किसी को मेट्रिक्स का बिल नहीं मिलना चाहिए।

जो मान कोई parse न कर सके वह **बंद** पढ़ा जाता है, विफलता कभी नहीं: यह ऑब्ज़र्वेबिलिटी है, और यहाँ का टाइपो ingestion endpoint के शुरू न होने का कारण नहीं होना चाहिए।

कॉलम दोनों तरह है। मेट्रिक *अभी* खरीदता है; कॉलम *कब से* और *किसके द्वारा* खरीदता है।

### दो आकार, एक अनुबंध

`shape` तय करता है कि **रिकॉर्ड** पंक्ति में क्या योगदान देता है। Producer का अनुबंध दोनों तरह वही है — envelope कभी नहीं बदलता। जो बदलता है वह तालिका का रूप है, जो ऑपरेटर का फ़ैसला है, producer का नहीं।

```yaml
shape: document   # the default
shape: columns
```

**`document`** के साथ, `data` पूरा एक `JSON` कॉलम में जाता है:

```
 brevis_ingestion_id | brevis_record_key | … | data
 d7179bfe-…          | A-1               |   | {"id": "A-1", "total": 150, "customer": {…}}
```

बनाने के बाद कभी कोई DDL नहीं होता। नया फ़ील्ड नई key है: कोई schema-change quota नहीं, write-stream दोबारा खोलना नहीं, replicas के बीच होड़ नहीं — और वह आने के दिन से query योग्य है। बाज़ार यहीं पहुँचा है: Fivetran, Airbyte और Snowpipe सब ऐसी raw परत में उतारते हैं जो विफल नहीं हो सकती और उसे आगे मॉडल करते हैं।

**`columns`** के साथ, रिकॉर्ड के हर फ़ील्ड को अपना कॉलम मिलता है:

```
 id   | total | customer              | items
 A-1  | 150   | {"id": 7, "uf": "SP"} | [{"qty": 2, "sku": "X"}]
```

**Scalars `STRING` बनते हैं, ऑब्जेक्ट और सूचियाँ `JSON` बनती हैं, और कहीं कोई अनुमान नहीं है।** `150` संख्या के रूप में आया और string `150` बना। यह सीमा नहीं बल्कि चुनाव है: जो फ़ील्ड आज पूर्ण और कल भिन्न आए, वह किसी के कुछ लिखे बिना कॉलम का टाइप बदल देता, और जो पंक्ति अब नहीं समाती वह dead letter में जाती है। किसी को टाइप देना **पदोन्नति** का रास्ता है — YAML में लिखा और diff में समीक्षित।

**`null` बिल्कुल कोई कॉलम नहीं बनता।** उसका कोई आकार नहीं, इसलिए पढ़ने को कुछ नहीं: फ़ील्ड तालिका में पहले उस event से दिखता है जो कोई मान ढोता है, उस मान के आकार के साथ। जिस event ने `null` भेजा वह भी `NULL` उतरता है, जो उसका अर्थ था — और जहाँ कॉलम पहले से है, क्योंकि आपने उसे घोषित किया या किसी दूसरे event ने बनाया, वहाँ `null` छोड़ने के बजाय उसमें लिखा जाता है।

**और `JSON` कॉलम JSON मान पाता है, उसकी string कभी नहीं।** यह ब्योरा लगता है और यही query योग्य तालिका और सही दिखने वाली तालिका का फ़र्क है:

```sql
SELECT JSON_TYPE(customer) FROM bronze.orders;     -- object
SELECT JSON_VALUE(customer, '$.uf') ...;           -- SP
```

अगर `JSON_TYPE` `string` कहे और `JSON_VALUE` NULL, तो ऑब्जेक्ट दो बार encode हुआ — कॉलम ऑब्जेक्ट की जगह ऑब्जेक्ट का TEXT थामे है। **0.15.0** तक के (और समेत) gateway संस्करणों ने BigQuery पर यह किया, **दोनों** आकारों में; एक-पंक्ति जाँच और मरम्मत के लिए [gateway changelog](https://github.com/AreteAcademy/brevis/blob/master/gateway/CHANGELOG.md) देखें। Postgres और MySQL कभी प्रभावित नहीं थे: उनका सर्वर टेक्स्ट को parse करता है।

फ़ील्ड का नाम BigQuery के नियम से मेल खाना चाहिए, जो चारों में सबसे सँकरा है। Postgres quoted होने पर लगभग कुछ भी स्वीकार कर लेगा, और यही जाल है: तालिका वहाँ बन जाती है और उस दिन टूटती है जब कोई stream BigQuery की ओर इंगित करे।

दोनों आकारों का SDK समतुल्य है, और वह मेल खाता कोड नहीं बल्कि वही कोड है: `sdk.Landing(table, sdk.LandingKey("id"))` `document` उतारता है, और [`sdk.LandingColumns()`](/docs/sdk/#द-आकर-पर-रकरड-य-हर-फलड-क-लए-एक-कलम) जोड़ने पर `columns`। वही कॉलम, वही मान, वही `brevis_ingestion_id` — इसलिए stream बिना migration के gateway और pipeline के बीच जा सकता है, और दोनों को एक तालिका की तरह पढ़ा जा सकता है।

Pipeline वही कॉलम उसी तरह बढ़ा सकती है: गंतव्य पर
[`Evolve: sdk.EvolveAdditiveFromPayload`](/docs/sdk/#जब-तलक-म-न-हन-वल-फलड-दख)
सेट करें और batch जो फ़ील्ड ढोता है वह इसी नियम से, इसी कोड से कॉलम बन जाता है।


**उपसर्ग कॉन्फ़िगर किया जा सकता है, और यह पूरी प्रोसेस का एक फ़ैसला है।** `BREVIS_LANDING_PREFIX=acme` उन्हें `acme_ingestion_id` और बाकी बना देता है — इस gateway का हर stream, इसलिए दो sinks उस तालिका पर असहमत नहीं हो सकते जिसमें वे दोनों लिख सकते हैं। ऊपर का आरक्षित नियम इसके साथ चलता है: `acme_` के तहत, `acme_region` भेजने वाला producer मना होता है और `brevis_region` सामान्य फ़ील्ड है।

**इसे पहली तालिका बनने से पहले चुनें।** बाद में बदलना आठ कॉलम जोड़ता है और आठ छोड़ देता है — कुछ कॉलम नहीं हटाता — इसलिए जिस तालिका में एक उपसर्ग के साथ लेआउट पहले से है वह दूसरा घोषित करने वाले लोड को मना करती है। सामान्यीकरण और बाहर निकलने के रास्ते के लिए [SDK का पेज](/docs/sdk/#brevis-उपसरग-बदलन-आपक-हथ-म-ह) देखें।

**नेस्टेड ऑब्जेक्ट के फ़ील्ड को अपने कॉलम मिल सकते हैं।** Gateway की प्रोसेस पर `BREVIS_NORMALIZE_DATA=true` एक स्तर समतल करता है — `{"name": {"first": "x"}}` `name_first` बनकर उतरता है — और हर stream इसे साझा करता है, इसलिए दो sinks उस तालिका पर असहमत नहीं हो सकते जिसमें वे दोनों लिख सकते हैं। यह क्या नाम बदलता है, जो इसे चालू करने से पहले पढ़ने वाला आधा है, उसके लिए [SDK का पेज](/docs/sdk/#हर-नसटड-फलड-क-लए-एक-कलम) देखें।

**BigQuery पर, `nationalID` और `nationalId` एक कॉलम हैं।** BigQuery कॉलम का केस मिला देता है, इसलिए जिस flush में `DELETE` में एक वर्तनी और `INSERT` में दूसरी है वह `CREATE` को विफल करने के बजाय **एक** कॉलम बनाता है — क्रमबद्ध क्रम में पहला। जो एक event दोनों ढोता है वह दोनों फ़ील्ड का नाम बताकर मना होता है। Postgres और MySQL उन्हें दो कॉलम रखते हैं, जो उनके लिए सही है। कौन-सी वर्तनी जीतती है और क्या migrate नहीं होता, इसके लिए [SDK का पेज](/docs/sdk/) देखें।

### `UPDATE` और `DELETE` दर्ज होते हैं, लागू नहीं

`brevis_operation` कॉलम है। लैंडिंग तालिका **इतिहास** है: जगह पर लागू `UPDATE` पिछला संस्करण खो देता है, और जिस दिन आपको वह संस्करण चाहिए वही दिन है जब कुछ टूटा। "हर रिकॉर्ड का वर्तमान संस्करण" resolve करना आगे के मॉडल का काम है:

```sql
qualify row_number() over (partition by brevis_record_key
                           order by brevis_received_at desc) = 1
   and brevis_operation <> 'DELETE'
```

जो Debezium, Fivetran और Airbyte करते हैं — और इसका मतलब है कि CDC write के रास्ते में **कुछ नहीं** खर्च करता: कोई upsert मोड नहीं, कोई lock नहीं, कोई delete नहीं।

### पहचान में घड़ी नहीं है

```
brevis_ingestion_id = uuid5(auto_table | table | data[unique_key] | sha256(canonical(data)))
```

सूत्र में कोई `occurred_at` नहीं है, और यह जानबूझकर है: आगमन **हमारी** ज़िम्मेदारी बन गया, और हमारी घड़ी `time.Now()` है — हर डिलीवरी पर अलग। जिस पहचान में वह होती वह हर दोबारा की डिलीवरी को नया event बना देती, और `merge` कुछ सोखना बंद कर देता।

```
same record, same content, twice  →  same id, merge absorbs
same record, content changed      →  a different id, both versions land
```

चेतावनी वही दस्तावेज़ी है: बाइट-समान `data` वाले दो सचमुच अलग events एक में सिमट जाते हैं। CDC के लिए यह **सही** है — एक रिकॉर्ड पर समान मानों वाले दो समान अपडेट एक ही तथ्य हैं।

Fingerprint दस्तावेज़ के **canonical** रूप पर है — हर स्तर पर keys क्रमबद्ध, सूचियाँ क्रम में, सब कुछ quoted। Go map iteration को यादृच्छिक करता है, इसलिए भोला hash एक ही event की दो डिलीवरी में अलग होता, जो वही एक चीज़ है जिसे रोकने के लिए वह है।

### तालिका अपने आप कॉलम बढ़ाती है

जिस `data` में ऐसा फ़ील्ड है जो तालिका में नहीं, उससे कॉलम प्रकट होता है। **योगात्मक, और केवल योगात्मक**: कुछ नहीं हटता, कुछ संकरा नहीं होता।

BigQuery पर यह **0.13.0** में ही सच हुआ। उससे पहले, `auto_table` हर गंतव्य पर `Evolve: additive` घोषित करता था और घोषणा केवल Postgres और MySQL drivers पढ़ते थे — इसलिए वहाँ आकार निर्माण पर जम गया था और कुछ नहीं कहता था।

> **0.11.0 या 0.12.0 से आ रहे हों तो**, `0.11.0` से पहले बनी तालिकाओं को एक बार की migration चाहिए, क्योंकि आठवाँ निश्चित कॉलम घोषणा में गया, तालिका में नहीं:
>
> ```sql
> ALTER TABLE `<dataset>.<table>` ADD COLUMN IF NOT EXISTS brevis_received_bytes INT64
> ```
>
> Postgres और MySQL कभी प्रभावित नहीं थे।

जो batch `ALTER` की होड़ हारता है वह विफल होता है, और pipe का सामान्य retry उसे सुलझा देता है — DDL की प्रतीक्षा करता batch और worker की प्रतीक्षा करता batch एक ही चीज़ हैं, इसलिए कोई दूसरा बफ़र नहीं। Replicas के पार metastore प्रति तालिका और प्रति आकार एक-सेकंड का *debounce* रखता है, ताकि वही नया फ़ील्ड देखते दस replicas BigQuery के प्रति तालिका प्रति दस सेकंड पाँच metadata ऑपरेशन के quota के विरुद्ध दस `ALTER` न बन जाएँ।

**Debounce, lock नहीं**: कुछ छोड़ा नहीं जाता और कोई lease नवीनीकृत नहीं होती। जो हारता है उसका *retry* होता है, उसे रोका नहीं जाता, और जो replica एक थामे हुए मर जाए वह दूसरों को एक सेकंड का खर्च देता है। बाज़ार यही करता है — Delta Lake और Iceberg आशावादी ढंग से commit करते और retry करते हैं, Kafka Connect क्रमबद्धता partition के क्रम से पाता है, Fivetran प्रति तालिका एक writer रखता है।

### नाम ही हमले की सतह है

`auto_table` payload की string को DDL बनाता है, इसलिए `naming` के तहत हर फ़ील्ड इनकार है:

- **`pattern`** का डिफ़ॉल्ट `^[a-z][a-z0-9_]{2,48}$` है — वह जो Postgres, MySQL, BigQuery और Redshift बिना quote के स्वीकार करते हैं, उसका प्रतिच्छेदन। जो नाम पास होता है उसे कहीं quoting नहीं चाहिए और वह injection नहीं ढो सकता।
- **`allow`** उपसर्ग से और सँकरा करता है।
- **`max_new_per_hour`** (डिफ़ॉल्ट 20) एक चलते घंटे में **निर्माण** को बाँधता है, writes को कभी नहीं: जो तालिका पहले से मौजूद है उस पर कभी दर-सीमा नहीं लगती। `metastore: memory` के तहत बजट प्रोसेस में रहता है और चार replicas इसका चार गुना स्वीकार करते हैं; `redis` या `memcached` के तहत वे एक counter साझा करते हैं।

नियमों से बाहर का नाम response में **प्रति event** मना होता है, और उसी अनुरोध का हर सही-बना event फिर भी उतरता है:

```json
{"accepted": 0, "rejected": ["event 0: the table name \"orders\" does not match ^(app|svc)_[a-z0-9_]{2,40}$"]}
```

जानबूझकर प्रति event, प्रति batch नहीं। इसे write के समय मना करना पूरे batch को विफल करता — एक producer का एक विकृत event उसी flush विंडो के हर दूसरे producer के events को दफ़ना देता, किसी को बताए बिना।

**बिना `listen.auth` वाले endpoint पर `auto_table` मना है**, `BREVIS_ENV=local` में भी। जो producer तालिका का नाम ले सकता है वह उसे बना सकता है, इसलिए उसे वहाँ भी authentication चाहिए जहाँ सामान्य stream को नहीं चाहिए। NetworkPolicy इसे कवर नहीं करती: वह सीमित करती है कि port तक कौन पहुँचे, यह नहीं कि वे किस तालिका का नाम माँगें।

### Metastore एक cache है

यह इसलिए है कि प्रति-event write प्रति-event lookup न हो, और यह तीन चीज़ें थामता है: कौन-सी तालिकाएँ मौजूद हैं, DDL *debounce*, और `max_new_per_hour` के पीछे का counter।

यह cache है और सत्य का स्रोत **नहीं**। तालिका मौजूद है या नहीं यह गंतव्य तय करता है, और एक बनाने के लिए होड़ करते *N* replicas सामान्य स्थिति है: `AlreadyExists` सफलता है, और यह सिर्फ़ होड़ घटाता है। TTL वह अवधि है जितनी देर यह **गलत** हो सकता है।

| backend | कब |
|---|---|
| `memory` | डिफ़ॉल्ट, कुछ नहीं माँगता। **एक** replica के साथ यही सही जवाब है |
| `redis` | `addr_from` ज़रूरी। `SETNX` claim है, `INCR` counter है |
| `memcached` | `addr_from` ज़रूरी। `Add` claim है, और expiry **सेकंड**-दानेदार है |

**Routing key वाले stream के लिए Redis तेज़ है**, और अंतर विशेषण से नहीं संख्या से बताने लायक है। प्रति तालिका trigger के साथ, विंडो प्रति तालिका एक claim माँगती है: Redis उनमें से 94 का जवाब एक pipelined round trip में देता है, और बाकी हर backend उन्हें समानांतर में, 32 पर बाँधकर। 25 ms दूर backend के विरुद्ध मापा — 94 claims:

| backend | एक-एक करके | जैसा शिप होता है |
|---|---:|---:|
| `redis` | 2.589 s | **29.8 ms** |
| `memcached` | 2.577 s | **89.4 ms** |

एक round trip के विरुद्ध तीन लहरें। दोनों claim को दिए 500 ms के भीतर हैं, इसलिए memcached धीमा चुनाव है, मना किया हुआ नहीं।

कई replicas के साथ, `memory` हर एक को अपना देता है: debounce कुछ debounce नहीं करता और `max_new_per_hour` एक प्रोसेस को बाँधता है। बाकी दो के होने का यही अकेला कारण है।

**गिरा हुआ metastore write को अपने साथ नहीं गिराना चाहिए।** त्रुटि देने वाला `Get` miss है, त्रुटि देने वाला claim जीता हुआ माना जाता है, त्रुटि देने वाला counter सीमा ढीली कर देता है। जो cache ingestion रोक सके वह बिना cache से बुरा है।

**और "गिरा हुआ" में "धीमा" शामिल है।** Claim तब चलता है जब बफ़र का lock थमा होता है, इसलिए जो backend जवाब देने में दो सेकंड लेता है वह दो सेकंड है जिनमें वह stream कुछ स्वीकार नहीं करता। हर कॉल **500 ms** पर बँधी है और उसके पार खुली विफल होती है। Redis इसे caller के context के रूप में मानता है; `gomemcache` कोई context नहीं लेता, इसलिए memcached इसे socket deadline के रूप में मानता है — दो तरीकों से पहुँची वही सीमा, क्योंकि दो में से एक क्लाइंट को रुकने को नहीं कहा जा सकता।

**`ttl: 0` का मतलब कभी नहीं**, और कुछ न कहने पर भी एक-मिनट का डिफ़ॉल्ट लगता है — दो नहीं, तीन स्थितियाँ।

गलत प्रविष्टि से उबारने वाली चीज़ घड़ी **नहीं** है। गंतव्य द्वारा मना किया गया write `invalidate` बुलाता है, जो प्रविष्टि तुरंत गिरा देता है और pipe के अपने retry को ठंडे cache के विरुद्ध लौटने देता है: हाथ से गिराई गई तालिका की कीमत **एक विफल प्रयास** है, एक मिनट नहीं।

Expiry की कीमत है। `sinkFor` `naming.max_new_per_hour` तब वसूलता है जब वह तालिका को **जानता** नहीं — इसलिए miss हफ़्तों से मौजूद तालिका के लिए भी वसूलता है, और तालिका के आखिरी event के एक मिनट बाद रीस्टार्ट होता pod उस साझा backend से कुछ नहीं पाता जिसकी वह कीमत चुका रहा है। लंबे समय तक जीने वाली तालिकाओं के साथ, `ttl: 0` ही जवाब है।

> `0.12.0` और `0.13.1` पर वह वर्तनी **parse नहीं होती** — YAML खाली शून्य को int पढ़ता है, और `0.13.2` पहला है जो दोनों लेता है। उन संस्करणों पर `ttl: 0s` लिखें।

`addr_from` पता रखने वाले **environment वेरिएबल** का नाम लेता है, पते का कभी नहीं: वह अक्सर पासवर्ड ढोता है।

### BigQuery की flush की निचली सीमा है

**BigQuery प्रति तालिका प्रति दिन 1,500 load jobs की अनुमति देता है।** डिफ़ॉल्ट एक-सेकंड की विंडो 86,400 है — quota का 57 गुना, करीब पच्चीस मिनट में खत्म। Pub/Sub के लिए यह समस्या नहीं है, और संख्या यहाँ Pub/Sub कॉन्फ़िग *से* पहुँचती।

इसलिए जो stream BigQuery में लिखता है, सीधे या router के ज़रिए, 60s से कम विंडो के साथ **लोड पर मना** होता है। असली जवाब Storage Write API है, जो लिखा नहीं गया; जब तक नहीं, कॉन्फ़िग उस विंडो से मना करता है जिसे वह निभा नहीं सकता, पहले deploy को पता चलने देने के बजाय।

`auto_table` **Redshift** में route नहीं कर सकता: वह driver तालिकाएँ नहीं बनाता, इसलिए हर नया नाम लोड पर विफल होता। नाम लेकर, शुरू होते समय मना।

## जो अभी मौजूद नहीं है

`sqlite`, `dynamodb`, `kinesis`, `sqs`, `sns`, `kafka` और `rabbitmq` योजना में हैं और कुछ लिखा नहीं गया। (`redis` और `memcached` **metastore** के रूप में मौजूद हैं, जो अलग चीज़ है: तालिकाओं के बारे में जो ज्ञात है उसका cache, गंतव्य कभी नहीं।) इस पेज के शुरू की तालिका हर एक की स्थिति बताती है, और स्थिति बदलने पर अपडेट होती है — उससे पहले नहीं।
