---
title: Ingestion
description: एक HTTP endpoint जो डेटा उतारता है — validate, पहचान, batch, डिलीवरी। अपनी image, अपना संस्करण।
group: Ingestion
order: 18
slug: ingestion
---

Ingestion gateway एक **HTTP endpoint है जो डेटा उतारता है**। क्लाइंट `POST` करता है,
`202` पाता है, और जो आया वह topic पर, तालिका में या bucket में रिकॉर्ड बन जाता है।

![Gateway का प्रवाह: /v1/clicks पर POST decode होता है, उसे ingestion_id मिलती है, तुरंत 202 से जवाब दिया जाता है और वह batch में जुड़ता है; worker batch को sink तक डिलीवर करता है, यहाँ files। Gateway का कोई डेटाबेस नहीं है।](/assets/flow-gateway.svg)

यह **अलग बाइनरी, अलग Go module, अलग संस्करण और अलग image** है। Engine ऑर्केस्ट्रेट करता है और ग्राहक के डेटा को कभी नहीं छूता; gateway और कुछ नहीं करता। नंबरिंग जानबूझकर स्वतंत्र है: engine 0.17 पर है और gateway 0.27 पर, जो सच्चा कथन है।

नीचे के खंड के `gateway.yaml` और `keys_from` में माँगी गई key के साथ:

```bash
docker run -p 8080:8080 -e BREVIS_INGEST_KEYS=dev-key \
  -v ./gateway.yaml:/etc/brevis/gateway.yaml:ro \
  brevissh/brevis-gateway:0.32.0
```

```text
2026/10/09 01:03:01 drain budget 30s; set terminationGracePeriodSeconds >= 40
2026/10/09 01:03:01 connections idle out after 1m30s and are recycled after 1m0s
2026/10/09 01:03:01 listening on :8080
2026/10/09 01:03:01 metrics on :9090/metrics
```

`POST` तुरंत `202` लौटाता है:

```bash
curl -X POST localhost:8080/v1/clicks -H 'Authorization: Bearer dev-key' \
  -d '{"event_id":"e-1","occurred_at":"2026-10-08T12:00:00Z","page":"/pricing"}'
```

```text
{"accepted":1,"rejected":null}
```

Google Cloud क्रेडेंशियल के बिना, batch का retry होता है और वह dead letter में जाता है — देखें [जब sink मना करे](#when-the-sink-refuses)।

`-slim` image सिर्फ़ `auto_table`, `files` और `postgres` sinks ढोती है; इस उदाहरण का `pubsub` और बाकी पूरी image में हैं। जो slim image ऐसा sink माँगे जो वह नहीं ढोती, वह शुरू होते समय कारण के साथ मना कर दी जाती है।

## फ़ाइल ही अनुबंध है

Hooks के सिवा लिखने को कोई कोड नहीं है। Streams, paths, फ़ॉर्मेट, पहचान, retry, गंतव्य और dead letter — सब YAML में, और जो फ़ील्ड निभाया न जा सके वह मना कर दिया जाता है।

```yaml
name: events_gateway

listen:
  addr: :8080
  max_body: 1MiB
  idle_timeout: 90s                  # the default; it cannot be turned off
  max_conn_age: 60s                  # off unless set — see below
  auth:
    type: bearer
    keys_from: BREVIS_INGEST_KEYS   # the NAME of the variable, never the keys

metrics:
  addr: :9090                        # its own port, never the ingest one

streams:
  - name: clicks
    path: /v1/clicks
    format: json                     # json | array | ndjson — declared, never sniffed

    identity:
      provider: web
      entity: click
      source_key: event_id
      record_ts: occurred_at

    buffer:
      flush: {every: 1s, records: 500}

    sink:
      type: pubsub
      project: acme-prod
      topic: clicks

    dead_letter:
      type: files
      path: /var/dead-letter/
```

`format` घोषित किया जाता है, अनुमानित नहीं: अनुमान लगाने से ही हज़ार का batch एक सूची वाली एक पंक्ति बन जाता है।

## `ingestion_id`, जो असली बात है

हर रिकॉर्ड एक `ingestion_id` के साथ निकलता है: `provider|entity|source_key|record_ts` पर **स्थिर UUID v5**।

यह वही **id** है जो batch fetcher उसी रिकॉर्ड के लिए निकालता है। जो पंक्ति gateway उतारता है और जो पंक्ति [SDK](/docs/sdk/) pipeline उतारती है, वे *एक ही पंक्ति* हैं, उनके बीच कोई मिलान नहीं।

दो व्यावहारिक नतीजे:

- **दोबारा भेजा गया `POST` वही रिकॉर्ड है।** क्लाइंट बिना डरे फिर भेज सकता है; `merge` वाला गंतव्य दूसरी पंक्ति लिखने के बजाय retry को सोख लेता है।
- **सूत्र स्थिर है।** चारों फ़ील्ड ज़रूरी हैं, और एक छोड़ देने से कमज़ोर नहीं बल्कि *अलग* id बनती है। फ़ाइल अधूरे कॉन्फ़िगरेशन को स्वीकार करने के बजाय मना करती है।

Ingestion के टूल बाइट बहुत अच्छे से ले जाते हैं और इस पर कोई राय नहीं रखते कि रिकॉर्ड *क्या है*। वह राय ही है जो gateway जोड़ता है।

## अनुरोध कभी sink का इंतज़ार नहीं करता

यह decode करता है, hook चलाता है, पहचान निकालता है, बफ़र में जोड़ता है और लौट आता है। **बस इतना।** भरा हुआ batch worker pool के पास जाता है, और publish, `COPY`, retries और dead letter सब वहीं होते हैं।

यह दिखावटी नहीं है। इसके होने से पहले, जो अनुरोध संयोग से batch भरता था वह डिलीवरी inline चलाता था — इसलिए हर `flush.records` में से एक caller पूरा round trip चुकाता था, और sink बंद हो तो पूरी retry विंडो। ऐसा p99 जिसका आकार इससे तय हो कि कौन-सा caller बदकिस्मत था, कोई नहीं सुधार सकता।

```yaml
buffer:
  flush:
    every: 1s         # age
    records: 500      # count
    size: 8MiB        # bytes — whichever is crossed FIRST sends the batch
    max_age: 30s      # and this one sends it REGARDLESS
  workers: 4          # batches delivered at once
  queue: 64           # full batches that may wait for a worker
  max_records: 10000  # events held in memory before the gateway says no
  max_bytes: 256MiB   # the same ceiling in bytes
```

### Flush की घड़ी deployment की है

`buffer.flush.every` **हर replica** में एक `time.AfterFunc` है। इसलिए तालिका को मिलने वाले load jobs replica की संख्या का फ़ंक्शन हैं, ट्रैफ़िक का नहीं:

```
jobs / day / table  =  replicas × 86400 / every
```

BigQuery पर, प्रति तालिका प्रति दिन 1,500 के विरुद्ध, **60s पर दो replicas पहले ही उस quota का 192% हैं जिसे बढ़ाया नहीं जा सकता** — HPA कुछ करे उससे पहले। और दिशा अंतर्ज्ञान से उलटी है: gateway HTTP concurrency के लिए बाहर की ओर फैलता है जबकि sink संकेंद्रण चाहता है।

```yaml
buffer:
  flush:
    every: 60s
    max_age: 300s
    claim: true
```

`claim` के साथ, replica timer पर flush करने से पहले stream और विंडो के लिए set-if-absent लेता है — और timer **विंडो की सीमा** पर जागता है, इसलिए हर replica एक ही key माँगता है। विंडो के हिसाब से एक flush, चाहे replicas कितने भी हों। और यह खुद को ठीक करता है: replica तभी इंतज़ार करता है जब सचमुच कोई दूसरा होड़ में है, इसलिए घटाने पर कम देरी लौट आती है, किसी को कॉन्फ़िग संपादित किए बिना।

दो replicas और एक Redis के साथ, 5s विंडो पर मापा: **8 load jobs 6 हो गए, हर विंडो के लिए एक, 3/3 बारी-बारी** — किसी विंडो में दो नहीं।

**इसे साझा backend चाहिए।** `memory` के साथ हर replica अपना claim खुद जीतता है और कुछ समन्वय नहीं करता। और यह **`max_age` माँगता है**, जिसके बिना कॉन्फ़िग मना करता है: claim `every` को लक्ष्य बना देता है, और बिना सीमा का लक्ष्य वह देरी है जिसे कुछ नहीं बाँधता।

**पहुँच से बाहर metastore फिर भी flush करता है।** समन्वय खोना load jobs की कीमत लेता है; flush से मना करना डेटा रोकता है। Run के बीच में Redis मारकर सत्यापित: 40 में से 40 events उतरे, कुछ नहीं रुका।

**`outcome="unreachable"` वह है जिस पर अलर्ट बनता है।** Backend से पूछा नहीं जा सका, इसलिए claim खुला विफल हुआ: समन्वय बंद है और load jobs फिर replica की संख्या से गुणा हो रहे हैं — चुपचाप, क्योंकि हर flush अब भी flush जैसा दिखता है।

और claim **विशिष्टता** देता है, निष्पक्षता नहीं: जो पहले माँगे वह जीतता है। असली दो-replica run में 4/1 मापा गया, बराबर बँटवारा नहीं। `max_age` वही है जो बदकिस्मत को भूखा मरने से रोकता है।

`claim: true` और `metastore: memory` के साथ gateway **boot पर एक बार** चेतावनी देता है: हर replica अपना claim जीतता है, इसलिए सब कुछ सही दिखता है और कुछ समन्वय नहीं करता।

### `every` लक्ष्य है; `max_age` वादा है

ऊपर के तीन triggers तय करते हैं कि batch कब निकलने की **कोशिश** करता है। `max_age` तय करता है कि वह कब **हर हाल में** निकलता है।

आज यह फ़र्क निष्क्रिय है, क्योंकि कुछ लक्ष्य को टालता नहीं। यह तब मायने रखने लगता है जब flush की घड़ी प्रति-प्रोसेस नहीं रहती: `buffer.flush.every` हर replica में एक `time.AfterFunc` है, इसलिए तालिका को मिलने वाले load jobs की संख्या **replica की संख्या** का फ़ंक्शन है, ट्रैफ़िक का नहीं — `replicas × 86400 / every`। BigQuery पर, प्रति तालिका प्रति दिन 1,500 के विरुद्ध, **60s पर दो replicas पहले ही quota का 192% हैं**।

एक बार जब replica विंडो दूसरे को दे सकता है, वह भरता रहता है, और `max_age` वही है जो किसी को अपनी बारी की प्रतीक्षा में डेटा अनिश्चित काल तक थामे रखने से रोकता है।

यह drain को भी तय करता है: `SIGTERM` पर बफ़र जो **थामे** हो सकता है वह इससे बँध जाता है, और gateway boot पर जो `terminationGracePeriodSeconds` छापता है वह इसी से निकला है।

लक्ष्य से **नीचे** की सीमा लोड पर मना की जाती है — उसका मतलब होता कि लक्ष्य कभी लागू नहीं होता।

### Size trigger क्यों है

बाकी दो **गिनती** हैं, और गिनती 2 KB के 500 events को 2 MB के 500 से अलग नहीं बता सकती — 1 MB बनाम 1 GB, फ़ाइल में वही संख्या। `max_records: 10000` का भी वही अंधा बिंदु है: 20 MB या 20 GB, और YAML में कुछ नहीं कह सकता कि कौन-सा।

जो producer id की जगह पूरा दस्तावेज़ भेजना शुरू कर दे, वह pod गिरा देता है, और इसके होने तक **कोई फ़ील्ड सीमा को व्यक्त नहीं कर सकता था**।

Size उस पर मापा जाता है जो **आया** — उस पर नहीं जो event मेमोरी में तौलता है (`map[string]any` अपने JSON से कई गुना होता है) और न उस hook के बाद जो उसे फुला दे। अनुमान है, और सही वाला: decode पर मुफ़्त है, payload के साथ चलता है, और जिससे बचाता है वह दस गुना बढ़ा रिकॉर्ड है।

**BigQuery पर `size` सीमा है, लक्ष्य नहीं।** वह गंतव्य प्रति तालिका प्रति दिन 1,500 load jobs की अनुमति देता है, इसीलिए `every` की निचली सीमा 60 सेकंड है। लेकिन वह सीमा **timer** पर लागू होती है: हर कुछ सेकंड में चलने वाला size सीधे उसके पार निकल जाता है और वही quota खर्च करता है। लोड पर कुछ इसे मना नहीं करता, क्योंकि आगमन दर वहाँ जानी नहीं जा सकती। इसके बदले जो है वह दृश्यता है:

```
brevis_gateway_flushes_total{stream,trigger}   trigger = time | records | size
```

BigQuery stream पर `trigger="size"` का चढ़ना आपको बता रहा है कि batching विंडो नहीं कर रही।

वह counter प्रति stream है, और quota प्रति **तालिका** है: `auto_table` के साथ एक route कई तालिकाएँ बन जाता है, और उनमें से 94 का एक संख्या रिपोर्ट करना उनमें से किसी के बारे में कुछ नहीं कहता। `BREVIS_INGESTION_METRICS` के पीछे एक है जो उसका नाम लेता है:

```
brevis_gateway_table_flushes_total{stream,table,trigger}
```

तालिका का एक flush उसके लिए एक load job है, इसलिए यही वह series है जिस पर BigQuery की अनुमत 1,500 में से 1,200 पर अलर्ट बनाना है। बिना शर्त वाला आधा boot पर एक पंक्ति है, जो event आने से पहले बताती है कि `flush.every` प्रति तालिका प्रति दिन कितना पड़ता है।

**यह सीमित है, fire-and-forget नहीं।** हर batch के लिए goroutine sink की गड़बड़ी को असीमित मेमोरी में बदल देता। जब कतार और बफ़र दोनों भरे हों, तो जवाब **`Retry-After` के साथ `503`** है, ऐसे event के लिए `202` नहीं जिसके पास जाने की जगह नहीं। स्वीकार किया गया event जो कभी डिलीवर न हो, वही नतीजा है जिसे न होने देने के लिए यह service है।

और वह `503` **दोबारा कोशिश के लिए सुरक्षित** है, ऐसे तरीके से जैसा लगभग कोई service नहीं कह सकती: `ingestion_id` event का ही स्थिर फ़ंक्शन है, इसलिए वही body दोबारा भेजना *वही रिकॉर्ड* है।

## कनेक्शन तय करता है कि भार किस replica को मिले

Kubernetes `ClusterIP` Service के पीछे, kube-proxy backend **हर TCP कनेक्शन पर एक बार** चुनता है, हर अनुरोध पर नहीं। ज़्यादातर क्लाइंट कनेक्शन जीवित रखते और दोबारा इस्तेमाल करते हैं, इसलिए मुट्ठी भर कनेक्शन वाला producer सब कुछ एक-दो replicas को भेजता है चाहे कितने चल रहे हों — और HPA जो replica जोड़ता है उसे कुछ नहीं मिलता, क्योंकि कोई उसे dial नहीं करता।

```yaml
listen:
  idle_timeout: 90s     # closes a connection nobody came back to
  max_conn_age: 60s     # after this, the next response asks the client to redial
```

**`max_conn_age` डिफ़ॉल्ट रूप से बंद है**, और यह जानबूझकर है: क्लाइंट एक कनेक्शन कितनी देर रख सकता है यह नीति है, और यह प्रति उम्र प्रति कनेक्शन एक in-cluster handshake के बदले भार का फैलाव देता है। अनुरोध खुद हमेशा परोसा जाता है — header response पर लगता है, कुछ दोबारा नहीं आज़माया जाता और कोई batch नहीं खोता।

**`idle_timeout` का डिफ़ॉल्ट है और उसे बंद नहीं किया जा सकता।** उसके बिना परोसना प्रोसेस के रीस्टार्ट तक हर छोड़े गए क्लाइंट के लिए एक file descriptor है। लंबा सेट करें अगर वही चाहिए; तब वह ऐसी संख्या है जिसे किसी ने चुना।

दोनों स्वतंत्र हैं: कनेक्शन उस सीमा से मिलता है जो पहले आए, और कनेक्शन उम्र तक सिर्फ़ इस्तेमाल होकर पहुँचता है — ठीक वही लंबे समय तक जीने वाला producer जिसे उम्र संतुलित करने के लिए है।

## जब sink मना करे

Batch का retry होता है — डिफ़ॉल्ट रूप से करीब सात सेकंड में चार प्रयास, jitter के साथ दोगुने — और फिर वह **dead letter** में जाता है, हर रिकॉर्ड पर कारण के साथ:

```json
{
  "event_id": "dl-9",
  "ingestion_id": "84aaee4b-66af-5cc0-b97b-6856dec95b25",
  "_dead_letter_reason": "rpc error: code = NotFound desc = Topic not found",
  "_dead_letter_sink": "pubsub:acme-prod/clicks",
  "_dead_letter_at": "2026-09-25T02:02:32Z"
}
```

कारण **रिकॉर्ड पर** चलता है, सिर्फ़ लॉग में नहीं, क्योंकि जिसे वह फ़ाइल बाद में मिलती है उसके पास events होते हैं, लॉग नहीं — और *यह यहाँ क्यों है* उसका पहला सवाल होता है।

**बिना `dead_letter` वाला stream लोड पर मना है।** इसे चुप्पी पर डिफ़ॉल्ट करना फ़ैसले को वहाँ रख देता जहाँ कोई उसे नहीं लेता, और जो मना किया गया batch सिर्फ़ लॉग पंक्ति है वह डेटा चुपचाप खो रहा है।

## Hook Go है, compile होकर भीतर

```go
hooks := gateway.NewHooks()
hooks.MustRegister("enrich_clicks", enrichClicks)
gateway.Main(hooks, gateway.WithSinks(sinks))

func enrichClicks(e map[string]any) (map[string]any, error) {
    host, _ := e["host"].(string)
    e["tenant"] = strings.Split(host, ".")[0]
    return e, nil     // returning nil DROPS the event, on purpose
}
```

YAML hook का नाम लेती है; उसे ढोती नहीं। चुनने से पहले मापा: Go फ़ंक्शन **93 ns/op** पड़ता है जबकि Starlark का 959 और yaegi का 1,281, बाइनरी में कुछ जुड़े बिना — और `plugin.Open` विकल्प ही नहीं है, क्योंकि `CGO_ENABLED=0` के तहत, जैसे यहाँ हर आर्टिफ़ैक्ट बनता है, वह `plugin: not implemented` लौटाता है।

इसकी कीमत, वहाँ कही गई जहाँ कोई पढ़ेगा: **hook जोड़ना rebuild और deploy है, कॉन्फ़िग बदलाव नहीं।** जब तक hooks वे लोग लिखते हैं जो बाइनरी शिप करते हैं, यह सही सौदा है, और उस दिन गलत जब ग्राहक को release के बिना एक बदलना हो।

Hook में त्रुटि *उस* event को dead-letter के रास्ते पर भेजती है और बगल के batch को कभी विफल नहीं करती। panic भी: वह प्रति event recover होता है, क्योंकि एक विकृत रिकॉर्ड उस प्रोसेस को नहीं गिराना चाहिए जो हर दूसरे stream को परोस रही है।

## जब event बहुत बड़ा हो

```yaml
oversize:
  larger_than: 256KiB
  archive: {type: files, path: gs://acme-oversize/clicks/}
  hook: strip_heavy_clicks
```

Event **पूरा** `archive` में लिखा जाता है, और घटाया हुआ संस्करण वापस इशारा करने वाले pointer के साथ stream में आगे जाता है:

```json
{
  "event_id": "big-1",
  "ingestion_id": "bd8ac083-9796-50f5-bab6-77a4e2e57ad0",
  "_oversize": true,
  "_oversize_archive": "files:gs://acme-oversize/clicks/",
  "_oversize_bytes": 2149
}
```

यह **Claim Check** पैटर्न है, और यह सपाट `413` से बेहतर है क्योंकि `413` event खो देता है — और बहुत बड़ा payload आम तौर पर किसी के पास सबसे दिलचस्प होता है: वह अनुरोध जिसमें पूरा दस्तावेज़ जुड़ा है, जो debug करने लायक मामला है।

`larger_than` एक **event** को मापता है, hook के बाद। `listen.max_body` अलग चीज़ है: वह पूरे **अनुरोध** को सीमित करता है और एक भी बाइट parse होने से पहले `413` से मना करता है।

## यह क्या गिनता है

ग्यारह Prometheus series, **अपने port** पर — ingest वाले पर कभी नहीं, और यह नियम यहाँ engine से ज़्यादा मायने रखता है: ingest port निर्माण से ही सार्वजनिक है, वहीं क्लाइंट `POST` करते हैं, इसलिए उस पर `/metrics` हर stream का नाम, path और गंतव्य उसे प्रकाशित करता जिसे path मिल जाए। दोनों के लिए वही पता देना लोड पर मना है।

```
brevis_gateway_events_received_total{stream,format}     counter
brevis_gateway_events_rejected_total{stream,reason}     counter  per event, after decode
brevis_gateway_requests_refused_total{stream,reason}    counter  per request: unauthorized|body_too_large|malformed|empty|saturated
brevis_gateway_batches_total{stream,sink,outcome}       counter  delivered|retried|buried
brevis_gateway_reconciled_total{stream,sink}            counter  landed after the send window; also delivered
brevis_gateway_flushes_total{stream,trigger}            counter  time|records|size
brevis_gateway_flush_windows_total{stream,outcome}      counter  won|yielded|ceiling|unreachable
brevis_gateway_ingested_bytes_total{stream,table}        counter  opt-in
brevis_gateway_ingested_events_total{stream,table}      counter  opt-in
brevis_gateway_table_flushes_total{stream,table,trigger} counter  opt-in
process_start_time_seconds                              gauge    unprefixed, on purpose
brevis_gateway_saturated_total{stream}                  counter  the 503s
brevis_gateway_delivery_seconds{stream,sink}            histogram
brevis_gateway_buffer_records{stream}                   gauge
brevis_gateway_queue_batches{stream}                    gauge
```

`process_start_time_seconds` पर `brevis_` उपसर्ग **नहीं** है, और यह जानबूझकर है: collectors ठीक यही नाम खोजते हैं। यह बताता है कि प्रोसेस कब शुरू हुई, और इसके बिना start-time adjustment करने वाला collector — OpenTelemetry Prometheus receiver, जिस पर Google Managed Prometheus बना है — हर cumulative series को उसके **पहले scrape** पर टिकाता है और उस scrape का मान आधार के रूप में खर्च कर देता है। नतीजा: हर counter कम पढ़ता है, deltas सही और कुल गलत।

आखिरी तीन वे हैं जिन्हें कोई पहली घटना के बाद तक नहीं माँगता। `saturated_total` अकेली संख्या है जो कहती है कि क्लाइंट को पीछे हटने को कहा गया; `buffer.max_records` के विरुद्ध `buffer_records` अकेली है जो उसकी **भविष्यवाणी** करती है।

`reason` बंद सेट है, त्रुटि का टेक्स्ट कभी नहीं: त्रुटि string तालिका का नाम, कॉलम, कभी-कभी पंक्ति ढोती है, और एक विकृत क्लाइंट हर अनुरोध पर नई series बना देता। टेक्स्ट dead letter में, रिकॉर्ड पर रहता है।

## `202`, `200` नहीं

Gateway ने events **स्वीकार** किए हैं। `durability: memory` के साथ — इकलौता लागू स्तर — वह जितना ईमानदारी से दावा कर सकता है वह बस इतना ही है। जो स्तर `200` कमाता है वह वह है जिसने उन्हें पहले ही लिख लिया हो, और वह मौजूद नहीं है।

`disk` **नाम लेकर** मना है, स्वीकार करके memory की तरह बरता नहीं जाता: `disk` लिखने वाला मानता है कि उसके events crash से बचेंगे, और बिना निभाए सहमत हो जाना ठीक वही विफलता है जिसे रोकने के लिए वह फ़ील्ड है।

Shutdown पर बफ़र में जो है वह डिलीवर होता है, गिराया नहीं जाता — उसी रास्ते से जो भरा batch लेता है, retry होकर और दफ़नाया जाकर।

## यह क्या नहीं करता

- **यह console में नहीं दिखता।** यह engine के इंटरफ़ेस के साथ कोई डेटाबेस साझा नहीं करता, इसलिए कोई `/ingestion` पेज नहीं है — और `auto_table` जो स्कीमा विकास करता है वह भी वहाँ नहीं दिखता। यह अगला कदम है और अभी लिखा नहीं गया।
- **`table` वाला stream स्कीमा विकसित नहीं करता।** जब आप तालिका का नाम लेते हैं, अनुबंध तालिका का होता है: जो फ़ील्ड उसके पास नहीं है वह उस संदेश के साथ मना किया जाता है जो उसे ठीक करता है, ऐसी तालिका बदलने के बजाय जिसकी किसी ने समीक्षा की थी। अपने आप कॉलम बढ़ाना वह है जो `auto_table` करता है, और दोनों में फ़र्क यही है।
- **`table` वाला stream कोई तालिका और कोई index नहीं बनाता।** जो service तालिकाएँ बनाती है वह टाइपो को दूसरी तालिका में बदल देती है जिसे कोई पढ़ नहीं रहा — इसीलिए निर्माण सिर्फ़ `auto_table` के तहत होता है, जहाँ `naming` बाँधता है कि कौन-से नाम मौजूद हो सकते हैं और `listen.auth` अनिवार्य है।
- **प्रकाशित image में कोई hooks नहीं हैं**, जो compile किए गए hook के डिज़ाइन के लिए ईमानदार आर्टिफ़ैक्ट है: वह उन streams को परोसती है जो `hook:` घोषित नहीं करते।

## एक route, N तालिकाएँ

`auto_table` हर event को उस तालिका पर route करता है जिसका नाम **envelope** लेता है, वह तालिका न हो तो बनाता है, और नया फ़ील्ड आने पर कॉलम बढ़ाता है:

```json
{"table_name": "app_orders", "operation": "INSERT", "unique_key": "id",
 "data": {"id": "A-1", "total": 150, "customer": {"id": 7, "uf": "SP"}}}
```

ऊपर नियंत्रण फ़ील्ड, रिकॉर्ड `data` के भीतर, और हर तालिका पर सात `brevis_*` ट्रैकिंग कॉलम। एक route, N तालिकाएँ, कुछ घोषित नहीं।

यह [Ingestion sinks](/docs/ingestion-sinks/#one-route-n-tables-nothing-declared) में है।

## आगे कहाँ जाएँ

| अगर आप चाहते हैं | यहाँ जाएँ |
|---|---|
| गंतव्य और हर एक कैसे लिखता है | [Ingestion sinks](/docs/ingestion-sinks/) |
| दूसरी ओर से `ingestion_id` | [Go SDK](/docs/sdk/) |
| डैशबोर्ड और अलर्ट | [ऑब्ज़र्वेबिलिटी](/docs/observability/) |
