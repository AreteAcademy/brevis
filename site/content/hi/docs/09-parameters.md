---
title: पैरामीटर
description: एक ही workflow के दो run के बीच, फ़ाइल को बदले बिना, क्या बदलता है।
group: संदर्भ
order: 9
slug: parameters
---

Workflow **run पैरामीटर** घोषित कर सकता है: ऐसे मान जो फ़ाइल बदले बिना दो ट्रिगर के बीच बदलते हैं।

## घोषित करना

```yaml
params:
  - name: load_full
    type: boolean
    default: "false"

  - name: start_date
    type: string
    pattern: '^\d{4}-\d{2}-\d{2}$'

steps:
  - id: run
    run: dbt build --vars '{"load_full":"{{ .load_full }}"}' --select bronze+
```

| फ़ील्ड | | |
|---|---|---|
| `name` | **ज़रूरी** | template और `--param` में इस्तेमाल होने वाला नाम |
| `type` | | `string`, `boolean`, `integer`, या कई मानों के लिए `list|<type>` |
| `default` | | ट्रिगर कोई मान न दे तो इस्तेमाल होता है |
| `pattern` | | वह रेगुलर एक्सप्रेशन जिससे मान मेल खाना चाहिए |
| `title` | | फ़ॉर्म में दिखने वाला लेबल, अधिकतम 60 अक्षर; वरना नाम |
| `group` | | फ़ॉर्म में पैरामीटर को इकट्ठा करता है, अधिकतम 60 अक्षर |
| `warn` | | डिफ़ॉल्ट से अलग मान की क्या कीमत है, फ़ील्ड के नीचे दिखता है, अधिकतम 200 अक्षर |
| `uses` | | उस connector की key जिसके बारे में पैरामीटर है (`bigquery`, `postgres`), उसके लोगो के लिए |

**बिना `default` वाला** पैरामीटर ज़रूरी है: जो ट्रिगर उसे छोड़ दे वह कोई भी step चलाने से पहले विफल हो जाता है।

`title`, `group`, `warn` और `uses` से फ़ॉर्म पैरामीटर को प्रस्तुत करता है, और **इनमें से कोई भी यह नहीं बदलता कि मान क्या हो सकता है**। हर एक एक पंक्ति है;
`uses` छोटे अक्षर, अंक और `-` है। Publish बाकी सब को मना करता है, पैरामीटर का नाम बताते हुए।

```yaml
params:
  - name: load_full
    type: boolean
    default: "false"
    title: Full load
    group: Load
    warn: reprocesses the whole table; it costs
    uses: bigquery
```

## मानों की सूची

`list|<type>` ऐसा पैरामीटर घोषित करता है जो कई मान ढोता है — लोड की जाने वाली तालिकाएँ, दोबारा बनने वाली परतें, backfill की विंडो:

```yaml
params:
  - name: tables
    type: list|string
    default: "users,orders"

  - name: layers
    type: list|string
    enum: [bronze, silver, gold]     # an enum restricts each ITEM
    default: "bronze"

  - name: days
    type: list|integer
    default: "1,7,30"
```

तत्व `string`, `integer` या `boolean` हो सकता है। हर आइटम अलग से validate होता है, इसलिए `list|integer` को `1,x,30` देने पर पूरे मान के बजाय **आइटम 2** का नाम लेकर विफल होता है।

यह हर जगह **अल्पविराम से अलग की गई एक string** है: डेटाबेस में, `BREVIS_RUN_PARAMS` में और कमांड में। इसी से यह बिना किसी विशेष मामले के step में काम करती है —

```yaml
run: dbt build --select {{ .tables }}      # dbt build --select users,orders
```

— और इसीलिए आइटम के भीतर अल्पविराम नहीं आ सकता। खाली आइटम और दोहराया गया आइटम भी मना हैं: दोहराव चुपचाप वह सब दोगुना कर देता है जो step प्रति आइटम करता है।

### Step में array के रूप में

```go
for _, table := range p.Run.ParamList("tables") {
    load(table)
}
```

```python
from brevis import run

for table in run.param_list("tables"):
    load(table)
```

जिस पैरामीटर को किसी ने नहीं भरा उसके लिए दोनों `[]` लौटाते हैं, एक खाली string वाली सूची नहीं: "करने को कुछ नहीं" पर iterate करने से कुछ नहीं होना चाहिए। आइटम strings के रूप में
लौटते हैं — `list|integer` समेत — क्योंकि संख्या का क्या करना है यह step लाइब्रेरी से बेहतर जानता है।

### इंटरफ़ेस में

`enum` वाली सूची checkboxes के रूप में दिखती है, जिन्हें JavaScript नहीं चाहिए और जिन्हें browser टिक किए गए हर बॉक्स के लिए एक मान के रूप में भेजता है। `enum` के बिना वह टेक्स्ट फ़ील्ड है जिसके आइटम हटाए
जा सकने वाले chips बनते हैं, और JavaScript बंद हो तो वह `users,orders` रखने वाला फ़ील्ड बना रहता है — जो ठीक वही है जिसकी सर्वर को अपेक्षा है।

### कमांड लाइन पर

```bash
brevis run wf.yaml --param tables=events,sessions --param days=1,30
```

:::note पुराना engine इसे नहीं पढ़ेगा
टाइप workflow के साथ सहेजा जाता है, और इस फ़ीचर से पहले का engine `list|string` को अज्ञात टाइप मानकर publish के समय मना कर देता है। इसे इस्तेमाल करने वाला workflow प्रकाशित करने से पहले engine अपग्रेड करें।
:::

## कमांड में इस्तेमाल करना

मान `run` में template से आता है, डबल ब्रेसेस में बिंदु के साथ:

```yaml
run: ./load.sh --since {{ .start_date }}
```

## कमांड लाइन पर देना

```bash
brevis run wf.yaml --param load_full=true
brevis run wf.yaml --param load_full=true --param start_date=2026-01-01
```

`--param` दोहराया जा सकता है। `=` के बिना इनपुट **त्रुटि** है:

```
erro: --param "load_full": use chave=valor
```

यह जानबूझकर है। `--param load_full`, मान भूलकर, डिफ़ॉल्ट के साथ चलता — और ऑपरेटर मान लेता कि backfill उस पैरामीटर के साथ हुआ जो उसने चाहा था।

## Backfill में

मान दायरे के **हर** slot पर लागू होते हैं। यही केंद्रीय उपयोग है: "पूरे जनवरी को `load_full=true` के साथ दोबारा प्रोसेस करो"।

```bash
brevis backfill daily --from 2026-01-01 --to 2026-01-31 --param load_full=true
```

## इंटरफ़ेस में

`params` वाले workflow को सरल ट्रिगर बटन की जगह **फ़ॉर्म** मिलता है। फ़ील्ड घोषणा से आते हैं, और `pattern` सबमिट से पहले validate होता है।

## Step के environment में

Run पैरामीटर हर step के **environment** में भी उपसर्ग के साथ आते हैं। यह इसलिए है कि [SDK](/docs/sdk/) से लिखा fetcher उन्हें बिना आर्ग्युमेंट के कुछ पाए देख सके:

```go
Before: func(ctx context.Context, p *sdk.Pipeline) error {
    if p.Run.Params["load_full"] == "true" {
        p.Source.URL += "&full=1"
    }
    return nil
},
```

## Auto params

हर run **auto params** भी ढोता है: ऐसे मान जो engine खुद निकालता है। इन्हें कोई घोषित नहीं करता, हर run में ये होते हैं, और ये run की स्क्रीन पर दिखते हैं।

जो मायने रखता है वह `adjusted_at` है, और जिस bug को यह हटाता है उसका नाम लेना ज़रूरी है। Fetcher `now()` पढ़ता है, उसमें से अपनी विंडो घटाता है और vendor से पिछले दो घंटे माँगता है। समय पर शुरू हुए run पर यह सही है। कतार
द्वारा चालीस मिनट देर किए गए run पर यह चालीस मिनट गलत है — और वे चालीस मिनट **किसी भी run के नहीं** हैं, क्योंकि अगला slot भी अपना `now()` पढ़ता है। कुछ विफल नहीं होता; डेटा बस गायब है, और हफ़्तों बाद मिलता है।

इसलिए engine घड़ी सौंपता है। जहाँ आप `now()` पढ़ते, वहाँ `adjusted_at` पढ़ें:

| param | | |
|---|---|---|
| `adjusted_at` | हमेशा | वह घड़ी जो यह run पढ़े: slot हो तो slot, वरना शुरुआत |
| `date` | हमेशा | `adjusted_at` को `YYYY-MM-DD` के रूप में, UTC — partition, फ़ोल्डर, `WHERE` |
| `delay_seconds` | हमेशा | यह प्रयास अपने slot से कितना देर से था; slot के बिना मैन्युअल run पर `0` |
| `previous_error` | हमेशा | इससे पहले का run सफल नहीं हुआ |
| `scheduled_at` | slot के साथ | वह slot जो cron ने माँगा |
| `started_at` | | यह प्रयास कब शुरू हुआ; retry इसे आगे खिसकाता है |
| `interval_start` | slot के साथ | **पिछला** slot |
| `interval_end` | slot के साथ | यह slot, **शामिल नहीं** |
| `previous_success_at` | | आखिरी सफल run का slot |

Run का slot तब होता है जब उसे शेड्यूल ने बनाया हो, backfill ने बनाया हो, या उसे **बीते slot के लिए** हाथ से ट्रिगर किया गया हो: ट्रिगर का `slot` फ़ील्ड (`2026-10-27T04:00:00Z`) उस slot को उसके अपने params के साथ दोबारा
चलाता है। जो slot cron कभी नहीं चलाता उसे मना किया जाता है, उससे पहले के सबसे नज़दीकी slot का नाम बताते हुए।

**यहाँ हर टाइमस्टैम्प UTC है**, `Z` के साथ RFC 3339 में लिखा, और run की स्क्रीन भी उन्हें UTC में दिखाती है — इसलिए पेज और `$BREVIS_AUTO_ADJUSTED_AT` कभी असहमत नहीं होते। `date` **UTC का दिन** है, जो उस workflow के लिए जिसका slot UTC की
आधी रात पार करता है, स्थानीय दिन नहीं है: `America/Sao_Paulo` में `0 22 * * *` का `date` अगले दिन का होता है। Airflow का `ds` उसी कारण से वैसे ही व्यवहार करता है — slot एक क्षण है, और क्षण का एक ही दिन तब होता है जब टाइमज़ोन चुना जाए।

`interval_start` और `interval_end` **cron** से आते हैं, इतिहास से नहीं: मार्च के slot का backfill वह विंडो बनाता है जो मार्च की थी, वह नहीं जो इस workflow के run आज बयान करते हैं। जो pipeline `[start, end)` माँगती है वह कभी ओवरलैप नहीं करती और
कभी खाली नहीं छोड़ती, चाहे वह कितनी भी देर से चले और कितनी भी बार retry हो।

ये **स्नैपशॉट** हैं, जो run शुरू होने पर निकाले जाकर उस पर सहेजे जाते हैं। `previous_error` उस क्षण के बारे में तथ्य है जब यह run शुरू हुआ; कल इसे दोबारा निकालना — पिछले run के retry होकर पास हो जाने के बाद — उसी नाम के साथ अलग सवाल का जवाब देता।

### उन्हें पढ़ना

Shell step में, हर मान के लिए एक वेरिएबल, ताकि कुछ parse न करना पड़े:

```yaml
steps:
  - id: extract
    run: |
      curl "https://api.example.com/events?since=$BREVIS_AUTO_INTERVAL_START&until=$BREVIS_AUTO_INTERVAL_END" \
        > "/data/$BREVIS_AUTO_DATE.json"
```

पूरा सेट `$BREVIS_AUTO_PARAMS` में एक JSON ऑब्जेक्ट के रूप में भी है, उस step के लिए जो एक बार parse करना पसंद करे।

Go में, [SDK](/docs/sdk/) उन्हें आपके लिए पढ़ता है:

```go
Before: func(ctx context.Context, p *sdk.Pipeline) error {
    start, end, ok := p.Run.Auto.Window()
    if !ok { // no schedule: fall back to a fixed window
        start, end = p.Run.Auto.Now().Add(-24*time.Hour), p.Run.Auto.Now()
    }
    p.Source.URL += "&from=" + start.Format(time.RFC3339) +
        "&to=" + end.Format(time.RFC3339)
    return nil
},
```

`Auto.Now()` वह घड़ी है जो `time.Now()` की जगह पढ़ी जाए। Engine के बाहर — हाथ से चलाए गए fetcher में — यह दीवार की घड़ी *ही* है, इसलिए स्थानीय विकास के लिए कुछ विशेष नहीं करना पड़ता।

Python में, `pip install brevis` वही मान पढ़ता है:

```python
from datetime import timedelta
from brevis import run

since, until = run.window() or (run.now() - timedelta(days=1), run.now())
df = fetch(since, until)
df.to_parquet(f"/data/{run.auto().date}.parquet")
```

Workflow का शेड्यूल न हो तो `window()` शून्य समयों की जोड़ी के बजाय `None` लौटाता है — epoch से query सब कुछ चुन लेती है, और वह विफलता चुप नहीं होनी चाहिए। ऊपर का `or` ही पूरा fallback है।

### dlt में, पढ़ने को कुछ नहीं

[dlt](https://dlthub.com) अपना cursor रखता है, उस स्थिति में जिसे वह गंतव्य पर लिखता है। वह स्थिति सिर्फ़ **आगे** बढ़ती है, जो तब तक ठीक है जब तक आप पुराना slot दोबारा न चलाएँ: cursor अब भी आज कहता है, इसलिए backfill आज की विंडो पढ़ता है, और
इसका dlt का अपना जवाब स्थिति को गिरा देना है।

Brevis जो विंडो सौंपता है वह cron से आती है, इसलिए मार्च का run मार्च की विंडो ही बनाता है चाहे कितनी भी बार retry हो। dlt उसे ले लेगा — नाम उसके अपने हैं, Airflow खोजने से पहले पढ़े जाते हैं, और engine उन्हें हर शेड्यूल्ड run पर सेट करता है:

```python
@dlt.resource(primary_key="id")
def events(
    updated_at=dlt.sources.incremental(
        "updated_at",
        initial_value=datetime(1970, 1, 1, tzinfo=timezone.utc),
        allow_external_schedulers=True,   # this line is the whole integration
    )
):
    yield from api.events(since=updated_at.start_value, until=updated_at.end_value)
```

`allow_external_schedulers=True` ही सब कुछ है। `$DLT_INTERVAL_START` और `$DLT_INTERVAL_END` dlt के `initial_value` और `end_value` बन जाते हैं, दोनों ओर अर्ध-खुले — ठीक शुरुआत पर बैठी पंक्ति भीतर है, ठीक अंत पर बैठी बाहर, जिससे लगातार slot न खाली छोड़ते हैं न ओवरलैप करते हैं।

`end_value` वाला incremental **सहेजी गई स्थिति को नहीं छूता**। यही मायने रखने वाली बात है: एक slot को दोबारा चलाना idempotent है, और मार्च का backfill उस cursor को नहीं खिसकाता जिस पर रात का run निर्भर है।

काम करने से पहले दो बातें जान लें:

- **Cursor datetime होना चाहिए, string नहीं।** dlt तुलना के लिए `text` cursor को ज़बरदस्ती नहीं बदलेगा और शेड्यूलर से जुड़ने से ही मना कर देता है, `JoinSchedulerError` के साथ जो यह साफ़ नहीं कहता। उसे resource पर बदलें — `add_map` — या ऊपर की तरह `initial_value` को टाइप करें।
- **बिना शेड्यूल वाला workflow कुछ नहीं सौंपता**, और तब dlt चुपचाप अपने cursor पर लौटने के बजाय `ExternalSchedulerNotAvailable` उठाता है। अगर pipeline को दोनों तरह चलना है, तो `allow_external_schedulers` बंद रखें और विंडो `run.window()` से खुद दें।

Step पर `tools: [dlt]` घोषित करना अलग बात है, और सिर्फ़ ग्राफ़ पर chip बनाता है — विंडो दोनों तरह सौंपी जाती है।

## स्नैपशॉट

पैरामीटर **run में** सहेजे जाते हैं, execution के समय workflow से नहीं पढ़े जाते। Run वह इनपुट ढोता है जिसके साथ वह ट्रिगर हुआ था, और जनवरी के execution का लॉग डिफ़ॉल्ट बदलने के बाद भी जनवरी के मान दिखाता रहता है।

## अगले कदम

- [कॉन्फ़िगरेशन](/docs/configuration/) — प्रोसेस के environment वेरिएबल
- [SDK](/docs/sdk/) — fetcher run का संदर्भ कैसे पढ़ता है
- [steps के बीच संदर्भ](/docs/context/) — step अगले के लिए क्या प्रकाशित करता है
