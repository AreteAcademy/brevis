# Python

> pip install brevis — steps के बीच संदर्भ, run की घड़ी और आपकी pipeline के अपने मेट्रिक्स, बिना किसी निर्भरता के।

*https://brevis.sh/hi/docs/python/ · brevis.sh docs (hi)*

---

```bash
pip install brevis
```

```python
from brevis import context, metrics, run

bucket = context.get("extract.bucket")
since, until = run.window()          # the window this run covers

context.set(rows=48213, watermark="2026-09-07T03:00:00Z")
metrics.set("rows_loaded", 48213)    # reaches the engine's /metrics
```

**कभी कोई निर्भरता नहीं।** यह एक environment वेरिएबल पढ़ती है, एक फ़ाइल लिखती है और stdout पर एक पंक्ति छापती है। Python 3.9+ चाहिए।

यह Go SDK का पोर्ट नहीं है: कोई drivers नहीं, कोई pagination नहीं, कोई ingestion ids नहीं। यह इसलिए है कि टीम pandas, Polars या dbt इस्तेमाल करे और फिर भी वह पाए जिसके लिए ऑर्केस्ट्रेटर होता है।

## संदर्भ पढ़ना

Key हमेशा उस step से योग्य होती है जिसने उसे प्रकाशित किया:

```python
context.get("extract.bucket")            # the value
context.get("extract.nope", default=0)   # absent is not an error
context.of("extract")                    # everything that step published
context.published()                      # what THIS step has published so far
```

बिना step वाला `context.get("bucket")` **मना** है, और उन steps के नाम बताता है जिन्होंने उसे प्रकाशित किया। Steps अलग-थलग हैं — `extract` और `transform` दोनों `bucket` प्रकाशित कर सकते हैं और कोई उसे खोता नहीं — और बिना step की key इसे उसी क्षण
गँवा देती है जब दो ऐसा करें। खोजकर एक चुनना संयोग से बनी प्राथमिकता है: यह तब तक काम करती है जब तक चुपचाप काम करना बंद नहीं कर देती।

जिस step पर यह निर्भर नहीं, उसे पढ़ना त्रुटि है, और संदेश सुधार की ओर इशारा करता है।

## लिखना

```python
context.set(name="Daniel")
context.set(label="Name")
# both survive: {"name": "Daniel", "label": "Name"}
```

कॉल **मर्ज** होती हैं। एक ही key दो बार हो तो आखिरी write रहता है; अलग key पहली को नहीं छूती। **step आर्ग्युमेंट नहीं है** — प्रोसेस जिस अकेले namespace में लिख सकती है वह उसका अपना है, जो अलगाव को नियम के बजाय संरचनात्मक बनाता है।

प्रोसेस के निकलने तक डिस्क पर कुछ नहीं पहुँचता, इसलिए जो step बुरी तरह मरता है वह कुछ प्रकाशित नहीं करता। यह सही है: क्रैश हुए step का आउटपुट उस काम का वर्णन करता जो पूरा नहीं हुआ।

## वह घड़ी जो यह run पढ़े

हर run **auto पैरामीटर** भी ढोता है: ऐसे मान जो engine खुद निकालता है। इन्हें कोई घोषित नहीं करता, हर run में ये होते हैं, और ये उस सवाल का जवाब देते हैं जो pipeline वरना `datetime.now()` से देती।

```python
from datetime import timedelta
from brevis import run

since, until = run.window() or (run.now() - timedelta(days=1), run.now())
df = fetch(since, until)
df.to_parquet(f"/data/{run.auto().date}.parquet")
```

:::warning `datetime.now()` क्यों नहीं
शेड्यूल्ड run पर `run.now()` **slot** है। जो fetcher `datetime.now()` पढ़कर दो घंटे घटाता है, वह समय पर शुरू हुए run पर सही है और कतार द्वारा देर किए गए run पर चालीस मिनट गलत — और वे चालीस मिनट **किसी भी run के नहीं** हैं, क्योंकि अगला slot भी अपना `now()` पढ़ता है। कुछ विफल नहीं होता, और खाली हिस्सा हफ़्तों बाद मिलता है।
:::

| | |
|---|---|
| `run.now()` | घड़ी: slot, या run कब शुरू हुआ |
| `run.window()` | `(start, end)`, end शामिल नहीं — या `None` |
| `run.auto()` | auto पैरामीटर, नीचे |
| `run.context()` | id, प्रयास, trigger, params और auto |
| `run.params()` · `run.param(name)` | workflow में घोषित पैरामीटर |
| `run.map_index()` · `run.map_value()` | `for_each:` के तहत इंस्टेंस |

`window()` **cron** से आती है, इतिहास से नहीं: मार्च के slot का backfill वह विंडो बनाता है जो मार्च की थी। `[start, end)` माँगना कभी ओवरलैप नहीं करता और कभी खाली नहीं छोड़ता, चाहे run कितना भी देर से हो और कितनी भी बार retry हो।

Workflow का शेड्यूल न हो तो यह शून्य समयों की जोड़ी के बजाय `None` लौटाती है — epoch से query सब कुछ चुन लेती है, और वह विफलता चुप नहीं होनी चाहिए। ऊपर का `or` ही पूरा fallback है।

### Auto पैरामीटर

| फ़ील्ड | |
|---|---|
| `date` | `"2026-09-08"` — partition, फ़ोल्डर, `WHERE` |
| `adjusted_at` | `now()` की जगह पढ़ी जाने वाली घड़ी |
| `scheduled_at` | वह slot जो cron ने माँगा; मैन्युअल run पर `None` |
| `started_at` | **यह प्रयास** कब शुरू हुआ; retry इसे खिसकाता है |
| `delay_seconds` | यह प्रयास कितना देर से था; कभी ऋणात्मक नहीं |
| `interval_start` · `interval_end` | विंडो, end शामिल नहीं |
| `previous_error` | इससे पहले का run सफल नहीं हुआ |
| `previous_success_at` | आखिरी **सफल** run का slot — जो `previous_error` को कार्रवाई योग्य बनाता है |

स्क्रिप्ट को हाथ से चलाएँ तो `run.now()` दीवार की घड़ी **है** और `window()` `None` है, इसलिए स्थानीय विकास के लिए लिखने को कोई branch नहीं।

## Mapped step का एक इंस्टेंस

[`for_each:`](/hi/docs/workflows/index.md) के तहत, engine हर तत्व के लिए एक प्रोसेस चलाता है और हर एक को बताता है कि उसे कौन-सा मिला:

```python
partition = run.map_value()
if partition is None:
    raise SystemExit("this step is meant to run under `for_each:`")

load(f"/data/{partition}.csv")
```

JSON string **बिना अपने उद्धरण चिह्नों के** आती है — `["2026-01"]` पर `for_each` step को `2026-01` देता है, `"2026-01"` नहीं। बाकी सब कुछ — ऑब्जेक्ट, संख्या, सूची — अपने JSON के रूप में आता है, इसलिए `json.loads(run.map_value())`।

Mapped न होने वाले step पर दोनों `None` हैं। `None`, और `-1` या `""` नहीं: sentinel वह संख्या है जिस पर कोई न कोई आखिर अंकगणित कर बैठता है, और खाली string उस तत्व से अलग नहीं पहचानी जा सकती जो *खुद* खाली string है।

Mapped step आगे **कोई संदर्भ** प्रकाशित नहीं करता — चार इंस्टेंस एक key साझा नहीं कर सकते। जिस step को कुछ आगे देना है वह फ़ाइल लिखता है, या उसके बाद का कोई step गिनता है कि क्या उतरा।

## वे मेट्रिक्स जो सिर्फ़ आपकी pipeline जानती है

Engine पहले से वह मापता है जो वह देख सकता है: अवधि, प्रयास, कतार की गहराई। जो उसके जानने का कोई तरीका नहीं है वह यह कि कितनी पंक्तियाँ उतरीं, vendor ने कितनी ठुकराईं, या कितने बाइट फेंके गए। Step यह जानता है, और लाइब्रेरी उसे ढोती है।

```python
from brevis import metrics

metrics.set("rows_loaded", 48213)          # a gauge: the last value wins
metrics.inc("vendor_rejected_total")       # a counter: it adds up
metrics.inc("bytes_discarded_total", 4096)
```

वे **engine के** `/metrics` से निकलते हैं, शेड्यूलर के port पर, workflow और step के label के साथ:

```
brevis_step_rows_loaded{workflow="daily_sales",step="load"} 48213
brevis_step_vendor_rejected_total{workflow="daily_sales",step="load"} 5
```

| | |
|---|---|
| `metrics.set(name, value)` | gauge। step के निकलने के बाद मान तब तक रहता है जब तक अगला run दूसरा न लिखे |
| `metrics.inc(name, by=1)` | counter। steps और runs के पार जुड़ता जाता है |

### यह port क्यों नहीं खोलता

क्योंकि **step scrape नहीं किया जा सकता**। `:9090` शेड्यूलर और API का है — लंबे जीने वाली प्रोसेस, स्थिर पते के साथ, हर पंद्रह से साठ सेकंड में scrape की जाती हैं। Step अपने pod में शुरू होता है, चालीस सेकंड चलता है और निकल जाता है: उसका खोला port कभी scrape नहीं होता, या संयोग से एक बार।

इसके तीन जाने-पहचाने जवाब हैं: Pushgateway (चलाने को एक घटक, और ऐसी series जो कोई हटाए तब तक रहती हैं), OTLP collector (एक घटक **और** इस लाइब्रेरी में निर्भरता, जिसकी कोई निर्भरता नहीं है और न होगी), या **ऑर्केस्ट्रेटर** से उन्हें ढुलवाना। Brevis पहले से हर step का stdout `@brevis:` पंक्तियों के लिए पढ़ता है — इसी से SDK के चरण ग्राफ़ तक पहुँचते हैं — और उसके पास पहले से meter और Prometheus endpoint है।

इसलिए लाइब्रेरी **stdout पर एक पंक्ति** लिखती है और बाकी engine करता है। मेट्रिक workflow और step के label के साथ मुफ़्त में पहुँचता है, क्योंकि engine जानता है कि वह क्या चला रहा था; Pushgateway के रास्ते लाइब्रेरी को खुद label लगाने पड़ते, और वह गलत करती।

Write `print(flush=True)` है, क्योंकि engine उस pipe को **लाइव** पढ़ता है। बफ़र हुई पंक्ति प्रोसेस के निकलने पर पहुँचती है — जो घंटे भर चलने वाले step के लिए घंटे भर देर है, और मार दिए गए step के लिए कभी नहीं।

### मेट्रिक क्या मना करता है

**अमान्य नाम ज़ोर से मना किया जाता है।** Prometheus अक्षर, अंक और अंडरस्कोर स्वीकार करता है, और पहला अक्षर अंक नहीं हो सकता। जिस नाम से वह मना करता है वह टूटा हुआ मेट्रिक नहीं बनाता — वह ऐसा exposition बनाता है जिसे वह **parse करने से मना कर देता है**, और **पूरा scrape** खो जाता है, बाकी हर मेट्रिक के साथ। इसलिए `rows-loaded` यहीं `MetricError` उठाता है, जहाँ गलती हुई, ऐसे नाम के नीचे पहुँचने के बजाय जो किसी ने लिखा ही नहीं।

| मना किया जाता है | क्योंकि |
|---|---|
| `rows-loaded`, `rows loaded`, `2rows` | Prometheus इसे parse नहीं करेगा, और पूरा scrape चला जाता है |
| `metrics.set("ok", True)` | Python में `bool` एक `int` है, और `1` रिपोर्ट करना ऐसी संख्या है जिसका अर्थ यह जानने पर निर्भर है। स्थिति नहीं, गिनती या अवधि रिपोर्ट करें |
| `metrics.inc("x", -1)` | counter सिर्फ़ ऊपर जाता है। उस पर हर `rate()` यही मानता है, और गड़बड़ हफ़्तों बाद ग्राफ़ में दिखती है |

स्क्रिप्ट हाथ से चलाएँ तो पंक्ति फिर भी stdout पर जाती है, जहाँ वह वही पढ़ी जाती है जो वह है। कोई उसे इकट्ठा नहीं करता और कुछ विफल नहीं होता — लैपटॉप पर `context.set()` की तरह।

## यह क्या मना करती है

| | क्योंकि |
|---|---|
| 4096 बाइट से ज़्यादा | प्लैटफ़ॉर्म मना करने के बजाय **छाँटता** है, इसलिए बड़ा ऑब्जेक्ट आधा कटा पहुँचता है और आगे "कुछ प्रकाशित नहीं हुआ" पढ़ा जाता है |
| ऐसा मान जो JSON नहीं है | `datetime` `"..."` बन जाता है या गायब हो जाता है, इस पर निर्भर कि serialize कौन करे। त्रुटि key का नाम बताती है और `.isoformat()` सुझाती है |
| string से अलग key | `{1: "x"}` `{"1": "x"}` बनकर लौटता है और lookup चूक जाता है |

सीमा प्लैटफ़ॉर्म की संख्या है, हमारी नहीं। "tasks के बीच संदर्भ" वाला हर फ़ीचर एक ही तरह विफल होता है — कोई **डेटा** वहाँ रख देता है जहाँ सिर्फ़ **संदर्भ** समाता है। चार किलोबाइट में watermark, गिनती या partitions की सूची समाती है। संदर्भ प्रकाशित करें और पंक्तियाँ वहीं छोड़ दें जहाँ हैं।

:::danger यह secret store नहीं है
जो कुछ प्रकाशित होता है वह run देख सकने वाले हर किसी को दिखता है — वह pod की स्थिति में और run के इतिहास में है। Path प्रकाशित करें, signed URL नहीं।
:::

## हाथ से चलाना

Brevis के बाहर कोई इनपुट नहीं होता और आउटपुट कोई नहीं पढ़ता। `get()` डिफ़ॉल्ट लौटाता है, `set()` स्वीकार करके फेंक देता है, और लाइब्रेरी यह INFO पर एक बार बताती है। यह **त्रुटि नहीं उठाती**: जिस स्क्रिप्ट को हाथ से चलाया नहीं जा सकता, उसे विकसित नहीं किया जा सकता। Validation फिर भी चलती है, इसलिए जो मान आपके लैपटॉप पर चलता है वह production में चलता है।

## अपने step का टेस्ट

पूरा अनुबंध दो environment वेरिएबल है, इसलिए mock करने को कुछ नहीं:

```python
def test_my_step(monkeypatch, tmp_path):
    monkeypatch.setenv("BREVIS_INPUT", '{"extract": {"rows": 42}}')
    monkeypatch.setenv("BREVIS_OUTPUT", str(tmp_path / "out"))
    assert context.get("extract.rows") == 42
```

## संस्करण

लाइब्रेरी का संस्करण अपना है, engine और Go SDK से अलग।

:::warning `0.2.0` इस्तेमाल न करें
यह PyPI पर सिर्फ़ wheel के रूप में गया। `0.2.1` वही कोड है, पूरा।
:::

`brevis.run` को पढ़ने के लिए कुछ पाने के लिए **`v0.9.0` या नए** engine की ज़रूरत है; पुराने पर हर फ़ील्ड खाली है और `run.now()` दीवार की घड़ी है — वही जो वह engine के बाहर करता है।

## अगले कदम

- [क्लाइंट लाइब्रेरी](/hi/docs/libraries/index.md) — अनुबंध और अन्य भाषाएँ
- [steps के बीच संदर्भ](/hi/docs/context/index.md) — अवधारणा
- [पैरामीटर](/hi/docs/parameters/index.md) — घोषित वाले
