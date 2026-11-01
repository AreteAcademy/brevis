# Go SDK

> Fetcher लिखना — स्रोत से डेटा निकालना, बदलना, और गंतव्य में लोड करना।

*https://brevis.sh/hi/docs/sdk/ · brevis.sh docs (hi)*

---

जब किसी step का काम **डेटा लाकर कहीं लोड करना** हो, तो Go SDK आपको पूरा fetcher कुछ पंक्तियों में दे देता है। यह अलग module है, जिसका संस्करण engine से स्वतंत्र है।

```bash
go get github.com/AreteAcademy/brevis/sdk@latest
```

Go 1.23 या नया चाहिए — SDK पंक्तियाँ `iter.Seq2` के रूप में स्ट्रीम करता है।

:::warning `v0.1.0` इस्तेमाल न करें
इसके साथ ऐसा `go.mod` गया जो ऐसे revision को pin करता है जो मौजूद नहीं है, और Go module proxy अपरिवर्तनीय है। `v0.1.1` से शुरू करें।
:::

## तीन चरण

```go
import (
	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/from"
	"github.com/AreteAcademy/brevis/sdk/to/bigquery"
)

rows, err := sdk.Extract(ctx, sdk.Source{
	From: from.HTTP{URL: "https://api.example.com/v1/events"},
})

rows = sdk.Transform(rows, sdk.Accept("id", "created_at", "amount"))

res, err := sdk.Load(ctx, rows, sdk.Target{
	To:      bigquery.Table{Dataset: "bronze", Name: "events"},
	Columns: []string{"id", "created_at", "amount"},
})
```

`Extract` पढ़ता है, `Transform` आकार बदलता है, `Load` लिखता है। हर एक अनुक्रम लेता और लौटाता है — कुछ भी एक साथ मेमोरी में materialise नहीं होता।

![SDK का प्रवाह: sdk.Extract, from.HTTP से HTTP API पढ़ता है, sdk.Transform स्वीकृत फ़ील्ड रखता है और sdk.Load पंक्तियों को BigQuery तालिका में लिखता है। तीनों कॉल आपके अपने step के भीतर चलती हैं।](/assets/flow-sdk.svg)

## Driver मान है, सेटिंग नहीं

`from.HTTP` वह सब ढोता है जो HTTP स्रोत को चाहिए: URL, headers, retry, pagination, और response का अर्थ क्या है। `from.Files` path और फ़ॉर्मेट ढोता है। किसी को दूसरे के फ़ील्ड के लिए जगह नहीं बनानी पड़ती, इसलिए ऐसा कोई स्रोत struct नहीं जिसमें चालीस विकल्प जमा हों और हर driver उनमें से छह पढ़े।

यह यह भी तय करता है कि **आप क्या compile करते हैं**। Go निर्भरताएँ import किए गए पैकेज के हिसाब से छाँटता है, इस्तेमाल किए गए फ़ील्ड के हिसाब से कभी नहीं:

| आप क्या import करते हैं | पैकेज | AWS | Google |
|---|---|---|---|
| `sdk` | 190 | नहीं | नहीं |
| `sdk` + `from` | 194 | नहीं | नहीं |
| `sdk` + `from` + `to` (files) | 195 | नहीं | नहीं |
| `sdk` + `to/bigquery` | 456 | नहीं | **हाँ** |
| `sdk` + `from` + `store/s3` | 265 | **हाँ** | नहीं |

पूरी फ़ाइल pipeline — पढ़ना और लिखना — 195 पैकेज की पड़ती है और किसी क्लाउड SDK की नहीं। **जिस driver के पीछे vendor SDK है वह अपने पैकेज में रहता है**, इसीलिए BigQuery `to/bigquery` है और ऑब्जेक्ट स्टोर `store/s3` और `store/gcs` हैं।

## स्रोत और गंतव्य

| स्रोत | पैकेज |
|---|---|
| HTTP | `from.HTTP` |
| फ़ाइलें (स्थानीय, S3, GCS) | `from.Files` |
| Postgres | `from/postgres` |
| MySQL | `from/mysql` |
| कई स्रोत | `from.Many` |

| गंतव्य | पैकेज |
|---|---|
| फ़ाइलें | `to.Files` |
| BigQuery | `to/bigquery` |
| Postgres | `to/postgres` |
| MySQL | `to/mysql` |
| Redshift | `to/redshift` |
| Pub/Sub | `to/pubsub` |

Path की स्कीम backend तय करती है, और `Store` driver के भीतर चुने जाने के बजाय **बाहर से दिया जाता है**:

```go
from.Files{Path: "s3://bucket/day=1/*.ndjson", Store: s3.New(client)}
to.Files{Path: "gs://bucket/landing/", Store: gcs.New(client)}
```

यही स्थानीय-फ़ाइल प्रोग्राम को AWS या Google के कोड की एक पंक्ति भी compile करने से रोकता है।

### Topic पर प्रकाशित करना

`to/pubsub` अकेला गंतव्य है जो तालिका नहीं है, और वह एक ऐसे तरीके से अलग व्यवहार करता है जो इस्तेमाल से पहले जानने लायक है: **वह जो भेजता है उसमें कुछ नहीं जोड़ता।**

```go
Target: sdk.Target{
    To: pubsub.Topic{
        Project: "acme-prod",
        Name:    "orders",

        // Nil is the default, and it means NO attributes at all.
        Attributes: func(e sdk.Envelope) map[string]string {
            row, ok := e.Payload.(map[string]any)
            if !ok {
                return nil
            }
            return map[string]string{"orderId": fmt.Sprint(row["order_id"])}
        },
    },
},
```

Subscriber को आपका payload बाइट-दर-बाइट मिलता है, साथ में ठीक वही attributes जिनका आपने नाम लिया। और कुछ नहीं — कोई `ingestion_id` नहीं, कोई `provider` नहीं, किसी तरह का कोई envelope नहीं।

यह नियम है, न्यूनतावाद नहीं। तालिका उसे लिखने वाली pipeline बनाती है, इसलिए SDK तय कर सकता है कि उसके कॉलम क्या हों। **Topic ऐसा नहीं है**: वह आपकी pipeline से पहले मौजूद है, उसके subscribers पहले लिखे गए और उनके filters भी पहले लिखे गए। अपने आप जोड़ा गया attribute Brevis का किसी और के अनुबंध को संपादित करना होता, और subscriber को रात के तीन बजे पता चलता।

इससे तीन बातें निकलती हैं:

- **यह कभी topic नहीं बनाता।** जो pipeline बना सकती है वह गलत भी बना सकती है, और गलत टाइप की तालिका के उलट किसी को पता नहीं चलता — संदेश कहीं चले जाते हैं और जिस subscriber को मिलने थे वह चुप रहता है।
- **`Schema`, `Dedup` और `PartitionBy` मना हैं**, विकल्प का नाम बताते हुए। वे तालिकाओं के लिए हैं: `Schema` से गंतव्य तालिका बनाता है, deduplication को बदलने के लिए पंक्ति चाहिए, partition तालिका का विचार है। यहाँ सबसे नज़दीकी `OrderingKey` है, जो अलग चीज़ है — वह उन संदेशों को समूहित करती है जिन्हें क्रम से *पहुँचना* है।
- **विफलता आंशिक होती है।** प्रकाशन लेनदेन नहीं है, इसलिए 31,000 पर विफल होने वाले 48,000 संदेशों ने 31,000 *डिलीवर* किए। नतीजा बताता है कि असल में क्या गया, और त्रुटि कहती है कि दोबारा चलाना दोबारा डिलीवरी है — subscriber को idempotent होना होगा, और Pub/Sub वैसे भी at-least-once है।

चलाने योग्य, emulator के विरुद्ध:
[`examples/13-pubsub`](https://github.com/AreteAcademy/brevis/tree/master/examples/13-pubsub)।

## पूरा fetcher

`sdk.Run` flags, `-dry-run`, लॉगिंग, retry, pagination, provenance, तालिका निर्माण और exit code सँभालता है। फ़ाइल में सिर्फ़ वही बचता है जो उस स्रोत के लिए खास है:

```go
package main

import (
	"time"

	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/from"
)

func main() {
	sdk.Run(sdk.Pipeline{
		Source: sdk.Source{
			From: from.HTTP{
				URL:     "https://api.example.com/v1/events",
				Timeout: 15 * time.Second,
			},
			Guard:  sdk.RejectIf("error"),
			Expand: sdk.ArrayAt("results"),
		},
		Target: sdk.Target{
			Provider: "example",
			Entity:   "events",
			Key:      sdk.Key("id"),
			When:     sdk.Field("created_at"),
		},
	})
}
```

```bash
go run ./fetcher -dry-run   # extracts, counts rows and errors, writes nothing
go run ./fetcher
```

## Run का संदर्भ

जब fetcher **workflow के step के रूप में** चलता है, तो engine environment में वह डालता है जो वरना वह नहीं जान सकता: क्या यह पहला run है, किन पैरामीटर के साथ भेजा गया, यह कौन-सा run है। SDK उसे `Pipeline.Run` में पढ़ता है:

```go
Before: func(ctx context.Context, p *sdk.Pipeline) error {
	if p.Run.Params["load_full"] == "true" {
		p.Source.From = from.HTTP{URL: base + "?full=1"}
	}
	return nil
},
```

हाथ से चलाने पर `Run` शून्य होकर लौटता है — इसे पढ़ना वैकल्पिक है, और अनदेखा करने की कोई कीमत नहीं।

इतिहास के बिना, "क्या यह पहला run है?" का जवाब हमेशा **नहीं** होता है: निश्चितता के बिना तालिका बनाना, न बनाने से बुरा है।

### घड़ी

`p.Run.Auto` वह है जो engine ने इस run के बारे में निकाला — [auto params](/hi/docs/parameters/#auto-params)। `Auto.Now()` वह घड़ी है जो `time.Now()` की जगह पढ़ी जाए: शेड्यूल्ड run पर वह slot है, इसलिए run के देर होने पर नहीं हिलती और तीन घंटे बाद retry होने पर भी नहीं हिलती।

```go
Before: func(ctx context.Context, p *sdk.Pipeline) error {
	start, end, ok := p.Run.Auto.Window()
	if !ok { // no schedule: fall back to a fixed window
		start, end = p.Run.Auto.Now().Add(-24*time.Hour), p.Run.Auto.Now()
	}
	p.Source.From = from.HTTP{URL: base +
		"?from=" + start.Format(time.RFC3339) +
		"&to=" + end.Format(time.RFC3339)}
	return nil
},
```

हाथ से चलाने पर `Auto.Now()` दीवार की घड़ी *है* और `Window()` `false` लौटाता है, इसलिए स्थानीय विकास को किसी विशेष मामले की ज़रूरत नहीं।

## क्लाइंट लाइब्रेरी से भ्रमित न हों

यह SDK Go में ETL मशीनरी है: drivers, pagination, provenance, तालिका निर्माण। [क्लाइंट लाइब्रेरी](/hi/docs/libraries/index.md) अलग चीज़ हैं — पतले क्लाइंट जो step को run का संदर्भ और घड़ी सौंपते हैं, आज Python में और बाद में Node.js और Rust में। pandas या dbt इस्तेमाल करने वाले step को दूसरी चाहिए, यह नहीं।

## वे पैरामीटर जो pipeline बनाते हैं

`p.Run.Params` pipeline के भीतर पढ़ने योग्य है, जो उस मान के लिए बहुत देर है जो तय करता है कि pipeline **क्या है** — हर राज्य के लिए एक स्रोत, हर tenant के लिए एक तालिका। `Pipeline.Flags` `Run` के भीतर parse होता है, इसलिए उसी कारण से बहुत देर है।

```go
func main() {
	sdk.Run(pipeline(sdk.ParamList("ufs")))
}
```

`sdk.Param` और `sdk.ParamList` पहले engine का environment पढ़ते हैं और दूसरे नंबर पर `-param name=value`:

```bash
./fetch-stations -param ufs=SP,RJ
```

Environment जीतता है — engine के तहत वही मान है, और manifest में छोड़ा गया flag उसे override नहीं करना चाहिए जो ऑपरेटर ने टाइप किया। Engine के बिना flag ही है जो उपलब्ध है, जो हर run पर `BREVIS_RUN_PARAMS` को JSON में लिखने से बेहतर है।

## लैंडिंग लेआउट

Pipeline **वही तालिका** उतार सकती है **जो gateway उतारता है** — वही कॉलम और वही `brevis_ingestion_id` — इसलिए दोनों में से चुनना deployment का फ़ैसला है, स्कीमा का नहीं। टीम दोनों को साथ पढ़ सकती है, और migration के बिना एक से दूसरे पर जा सकती है।

```go
const table = "landing.orders"

sdk.Run(sdk.Pipeline{
    Source:    sdk.Source{From: from.HTTP(/* ... */)},
    Transform: []sdk.Transformer{sdk.Landing(table, sdk.LandingKey("id"))},
    Target: sdk.Target{
        To:     postgres.Table{DSN: dsn, Name: table, CreateTable: true},
        Schema: sdk.LandingSchema(sdk.LandingOptions{}),
    },
})
```

`sdk.LandingSchema` नौ कॉलम है: आठ जो लेआउट के हैं और एक जो producer का। `sdk.Landing` उन्हें भरता है और रिकॉर्ड को पूरा `data` में रखता है। चाहें तो स्कीमा में अपने कॉलम जोड़ें, या `sdk.LandingControlColumns` लें और रिकॉर्ड के फ़ील्ड को अपने कॉलम दें।

### दो आकार: पूरा रिकॉर्ड, या हर फ़ील्ड के लिए एक कॉलम

ऊपर वाला **document** आकार है — रिकॉर्ड JSON के रूप में `data` में जाता है, और तालिका वही रहती है चाहे रिकॉर्ड में कुछ भी हो। `sdk.LandingColumns()` जोड़ें और रिकॉर्ड के हर फ़ील्ड को अपना कॉलम मिलता है:

```go
sdk.Landing(table, sdk.LandingKey("source_key"), sdk.LandingColumns())
```

तब तालिका `sdk.LandingControlColumns` और आपके कॉलम हैं — `sdk.LandingSchema` **नहीं**, जो `data` ढोता है:

```go
Target{
    To: postgres.Table{DSN: dsn, Name: table, CreateTable: true},
    Schema: append(sdk.LandingControlColumns(sdk.LandingOptions{Keyed: true}),
        sdk.Column{Name: "source_key", Type: sdk.TypeString},
        sdk.Column{Name: "series", Type: sdk.TypeString},
        sdk.Column{Name: "data", Type: sdk.TypeString},
        sdk.Column{Name: "valor", Type: sdk.TypeString},
    ),
}
```

**वे कॉलम आप घोषित करते हैं।** SDK कभी स्कीमा का अनुमान नहीं लगाता, और यह विकल्प उसे नहीं बदलता: यह तय करता है कि *पंक्ति* कैसे बनती है, तालिका क्या है यह कभी नहीं। Gateway अनुमान लगाता है क्योंकि उसका producer अजनबी है जो जो भी उसके पास है पोस्ट करता है; यहाँ pipeline का लेखक आकार जानता है और उसे लिख देता है, ऐसे diff में जिसकी कोई समीक्षा करता है।

यह वही आकार है जो gateway [`shape: columns`](/hi/docs/ingestion-sinks/#two-shapes-one-contract) के रूप में उतारता है, उसी कोड से — वही कॉलम, वही मान, वही `brevis_ingestion_id`।

**फ़ील्ड का नाम कॉलम का नाम हो सकने लायक होना चाहिए**: अक्षर या अंडरस्कोर, फिर अक्षर, अंक और अंडरस्कोर। यह BigQuery का नियम है, जो चारों गंतव्यों में सबसे सँकरा है, इसलिए जो नाम यहाँ पास होता है वह हर जगह चलता है। `my-field` ढोता रिकॉर्ड कुछ भी लिखे जाने से पहले नाम लेकर मना किया जाता है। Document आकार में ऐसा कोई नियम नहीं — हाइफ़न बिल्कुल वैध JSON key है।

**डिफ़ॉल्ट रूप से यह समतल नहीं करता।** नेस्टेड ऑब्जेक्ट उस key के नीचे **एक** `JSON` कॉलम बनता है जिसने उसे थामा था: `customer` में `{"id": 7, "uf": "SP"}` है, `customer_id` और `customer_uf` नहीं। सूची भी `JSON` है। जिस रिकॉर्ड को *सूची के हर तत्व* के लिए एक पंक्ति चाहिए, उसे स्रोत के `Expand` में `sdk.ArrayAt` चाहिए — वह `Landing` से पहले चलता है, और अलग नाम वाला अलग ऑपरेशन है। [`BREVIS_NORMALIZE_DATA`](#one-column-per-nested-field) पूरे deployment के लिए उसका पहला आधा बदलता है; सूची दोनों तरह `JSON` रहती है:

```go
Source: sdk.Source{From: from.HTTP(/* ... */), Expand: sdk.ArrayAt("results")},
```

**`null` `NULL` रहता है**, `""` कभी नहीं। null भेजा गया फ़ील्ड और खाली भेजा गया फ़ील्ड अलग तथ्य हैं, और कॉलम बाद में उन्हें अलग नहीं बता सकता।

**`data` आपका हो जाता है।** इस आकार में लेआउट के पास इस नाम का कॉलम नहीं है, इसलिए जिस producer के पास `data` नाम का फ़ील्ड है — जो ठीक वही है जो Bacen की series भेजती है — उसे अपना कॉलम मिलता है, अपने मान के साथ। `document` के तहत उनका वह लेआउट के JSON के भीतर एक key है।

### `brevis_` उपसर्ग बदलना आपके हाथ में है

```
BREVIS_LANDING_PREFIX=acme
```

और लेआउट के आठ कॉलम `acme_ingestion_id`, `acme_record_key` और बाकी बन जाते हैं। सेट न हो, तो वे वही रहते हैं जो हमेशा थे।

**इसे तब चुनें जब तालिका बन रही हो, क्योंकि बाद में बदलना नाम बदलना नहीं है।** इस SDK में कुछ भी कभी कॉलम नहीं हटाता, इसलिए उपसर्ग बदलने वाला stream आठ का नाम नहीं बदलता — वह आठ जोड़ता है और आठ छोड़ देता है। उसके बाद की हर पंक्ति में पुराने सेट में NULL होते हैं, और

```sql
qualify row_number() over (partition by brevis_record_key
                           order by brevis_received_at desc) = 1
```

ऐसे कॉलम पर partition करता है जिसे अब कोई नहीं लिखता। पंक्तियाँ आती हैं, डैशबोर्ड सपाट हो जाता है, और कुछ कुछ लॉग नहीं करता।

इसलिए जिस तालिका में एक उपसर्ग के साथ लेआउट पहले से है, वह दूसरा घोषित करने वाले लोड को extract से पहले, दोनों के नाम बताते हुए, **मना** करती है। उसे नई तालिका पर इंगित करें, या पहले आठ कॉलम खुद का नाम बदलें।

**आप जो भी टाइप करें वह सामान्यीकृत होता है**, ताकि गलत लिखने के चार तरीकों की जगह एक ही वर्तनी रहे:

| आप सेट करते हैं | आपको मिलता है |
|---|---|
| कुछ नहीं, या खाली | `brevis_` |
| `_NAME` | `name_` |
| `_NAME_` | `name_` |
| `NAME` | `name_` |

दोनों सिरों से `_` हटाएँ, छोटे अक्षर करें, ठीक एक जोड़ें। दो मान अनुमान लगाए जाने के बजाय मना किए जाते हैं: सिर्फ़ अंडरस्कोर, क्योंकि उससे `ingestion_id` बचता — ठीक वही कॉलम जो `sdk.IngestionID()` हर सामान्य pipeline के लिए लिखता है — और कोई भी चीज़ जो कॉलम के नाम की शुरुआत नहीं हो सकती। **अनुपयोगी मान वाली प्रोसेस शुरू नहीं होती**, जो इकलौता ईमानदार नतीजा है: विकल्प वे तालिकाएँ हैं जिनका नाम उस उपसर्ग पर पड़ता जिसे आप बदल रहे थे।

**यह प्रति प्रोसेस है, और यही मुद्दा है, सीमा नहीं।** एक gateway का हर stream इसे साझा करता है, इसलिए दो sinks असहमत नहीं हो सकते — और न वह स्कीमा जो तालिका घोषित करती है और वह transformer जो उसे भरता है।

**आरक्षित नियम इसके साथ चलता है।** `acme_` के तहत, `acme_region` भेजने वाला producer मना किया जाता है, क्योंकि जो control कॉलम गढ़ सकता है वह असली-सा दिखने वाला गढ़ता है। और `brevis_region` सामान्य फ़ील्ड बन जाता है।

`data` कभी उपसर्ग नहीं ढोता। वह producer का कॉलम है, लेआउट का नहीं।

### हर नेस्टेड फ़ील्ड के लिए एक कॉलम

```
BREVIS_NORMALIZE_DATA=true
```

और नेस्टेड ऑब्जेक्ट के फ़ील्ड को अपने कॉलम मिलते हैं, जैसा pandas का `json_normalize` करता है — एक स्तर:

```python
data = [
    {"id": 1, "name": {"first": "Coleen", "last": "Volk"}},
    {"name": {"given": "Mark", "family": "Regner"}},
    {"id": 2, "name": "Faye Raker"},
]
```

```
id  name_first  name_last  name_given  name_family  name
```

**इसे पहली तालिका से पहले चुनें, क्योंकि यह सिर्फ़ कॉलम जोड़ता नहीं।** यह उनका नाम बदलता है, और इस SDK में कुछ भी कभी एक को नहीं हटाता — इसलिए पुराना नाम उन पंक्तियों के साथ रह जाता है जो उसमें हैं जबकि बाद का सब कुछ नए में उतरता है, और जो query किसी ने उस पर लिखी थी वह चलती रहती है और नई पंक्तियाँ देखना बंद कर देती है। दो नाम बदलते हैं:

- `userName` `username` बन जाता है, चाहे कुछ नेस्टेड हो या न हो
- `name`, जिसमें ऑब्जेक्ट थे, `name_first` और `name_last` से बदल जाता है

जिस तालिका में दोनों में से कोई आकार पहले से है वह इसके चालू होने पर लोड को extract से पहले, दोनों कॉलम का नाम बताते हुए **मना** करती है। उसे नई तालिका पर इंगित करें, या पहले कॉलम खुद का नाम बदलें।

**`name` समतल और पूरा, दोनों रूप में दिख सकता है**, और ऊपर का उदाहरण यह दिखाता है: तीसरे रिकॉर्ड में `name` string है, इसलिए तालिका में `name_first`, `name_last` और `name` तीनों हैं। जो फ़ील्ड एक रिकॉर्ड में ऑब्जेक्ट और दूसरे में scalar है वह दोनों बनाता है।

**एक स्तर, और सूचियाँ मान हैं।** `{"a": {"b": {"c": 1}}}` `a_b` है जिसमें `{"c": 1}` है — `JSON` कॉलम, `a_b_c` नहीं। `{"a": [1, 2]}` `a` है, वह भी `JSON`। जिस रिकॉर्ड को सूची के हर तत्व के लिए एक पंक्ति चाहिए उसे अब भी `sdk.ArrayAt` चाहिए।

**जोड़कर और छोटे अक्षरों में, camelCase पर बाँटकर नहीं।** `userName` `username` बनता है, `user_name` नहीं, क्योंकि दूसरी पढ़ाई का संक्षिप्त शब्दों — `HTTPStatus`, `userID` — पर कोई सहमत जवाब नहीं है, और हर लाइब्रेरी उन पर असहमत है।

**टकराव मना किया जाता है, कभी सुलझाया नहीं जाता।** `{"name_first": 1, "name": {"first": 2}}` दो फ़ील्ड से एक कॉलम बनाता, और Go की map iteration यादृच्छिक है: एक को जीतने देना उसी batch को अलग run पर अलग तालिका देता। इनकार दोनों का नाम बताता है।

**आरक्षित उपसर्ग कॉलम पर जाँचा जाता है, फ़ील्ड पर नहीं।** `{"brevis": {"stream": "x"}}` भेजने वाला producer वरना `brevis_stream` उस दरवाज़े से बना लेता जिसे फ़ील्ड जाँच नहीं देखती। इसे `brevis.stream` का नाम लेकर मना किया जाता है ताकि वे फ़ील्ड ढूँढ सकें।

**यह प्रति प्रोसेस है**, उपसर्ग की तरह, और उसी कारण से: एक gateway का हर stream इसे साझा करता है, इसलिए दो sinks उस तालिका पर असहमत नहीं हो सकते जिसमें वे दोनों लिख सकते हैं। यह सिर्फ़ `columns` आकार पर लागू होता है — `document` के तहत रिकॉर्ड पूरा एक `JSON` कॉलम में जाता है और समतल करने को कुछ नहीं।

### वह टेक्स्ट जो पहले से JSON है

`sdk.TypeJSON` घोषित कॉलम JSON **मान** लेता है — ऑब्जेक्ट, सूची, संख्या। BigQuery पर पंक्ति पूरी encode होती है, इसलिए वहाँ Go `string` JSON string literal बन जाता है और कॉलम में आपके ऑब्जेक्ट का ऑब्जेक्ट नहीं बल्कि उसका TEXT रह जाता है:

```sql
SELECT JSON_TYPE(payload) ...;            -- "string", not "object"
SELECT JSON_VALUE(payload, '$.a') ...;    -- NULL
```

कॉलम सही टाइप का है और उसका डेटा पहुँच से बाहर, जो तीनों नतीजों में सबसे बुरा है क्योंकि कुछ विफल नहीं होता। **BigQuery गंतव्य इसे** `v0.74.0` से, कॉलम का नाम बताते हुए, **मना करता है**।

जहाँ ऑब्जेक्ट आपके पास है, वहाँ ऑब्जेक्ट ही दें। जहाँ आपके पास पहले से JSON टेक्स्ट है — किसी दूसरे डेटाबेस से पढ़ा कॉलम, response body जिसे आप दोबारा parse नहीं करना चाहते — वहाँ यह कहें:

```go
row["payload"] = sdk.JSONText(body)
```

`encoding/json` का अपना `json.RawMessage` भी चलता है, उसी कारण से: दोनों वह JSON लिखते हैं जो वे थामे हैं, उसकी quoted string नहीं।

**Postgres और MySQL प्रभावित नहीं हैं और मना नहीं किए जाते।** उनका सर्वर string को JSON कॉलम में parse करता है, और हमेशा से करता आया है।

### जब तालिका में न होने वाला फ़ील्ड दिखे

Producer फ़ील्ड जोड़ता है और लोड रुक जाता है, उसका नाम बताते हुए: पंक्ति ऐसा कॉलम ढोती है जिसे किसी ने घोषित नहीं किया, और उसे ऐसे गंतव्य में लिखना जिसने उसका ज़िक्र नहीं किया उसे चुपचाप गिरा देता। यह इनकार डिफ़ॉल्ट रूप से सही है — लेकिन लैंडिंग तालिका पर यही अकेला बदलाव है जो पाठक को नहीं तोड़ सकता, और गंतव्य इसे कर सकता है:

```go
postgres.Table{DSN: dsn, Name: table, Evolve: sdk.EvolveAdditiveFromPayload}
```

तब batch आपकी घोषणा को **पूरा** करता है: जो फ़ील्ड वह ढोता है और घोषणा नहीं, वह कॉलम बनता है, `STRING` जब तक फ़ील्ड ऑब्जेक्ट या सूची न हो, और तब `JSON`। यह वही नियम और वही कोड है जिससे gateway `shape: columns` उतारता है, इसलिए दोनों वही तालिका बनाते हैं।

दो और मान, और उनके बीच का फ़र्क यह है कि कॉलम कहाँ से आता है:

| `Evolve` | जोड़ता है |
|---|---|
| `sdk.EvolveNone` (डिफ़ॉल्ट) | कुछ नहीं। कोई भी फ़र्क मना, दोनों पक्षों का नाम बताते हुए |
| `sdk.EvolveAdditive` | वह कॉलम जो आपकी **घोषणा** में है और तालिका में नहीं |
| `sdk.EvolveAdditiveFromPayload` | वह, और वह कॉलम जो **batch** ढोता है |

**केवल `ADD COLUMN`।** जो कॉलम स्रोत से गायब हो जाए वह लिखा जाना बंद करता है और तालिका में रहता है; कुछ का नाम नहीं बदलता, कुछ नहीं हटता, और कोई टाइप कभी संकरा नहीं होता। यह कमी नहीं है — लैंडिंग तालिका इतिहास है, और जो अकेला बदलाव उसे पढ़ने वाले को नहीं तोड़ सकता वह नया nullable कॉलम है।

**यह घोषणा को पूरा करता है; बदलता नहीं।** कुछ घोषित न हो तो इसे extract से पहले, तालिका का नाम बताते हुए मना किया जाता है: payload से पूरी तालिका बनाना अलग फ़ैसला है, और SDK उसे नहीं लेता। लैंडिंग लेआउट पर नुकसान ठोस होता — `brevis_received_at` टाइमस्टैम्प की जगह टेक्स्ट निकलता, और `brevis_loaded_at` पंक्ति में कभी चलता ही नहीं, इसलिए end-to-end देरी का माप बस गायब होता।

**SDK अब भी अनुमान नहीं लगाता।** टाइप फ़ील्ड के आकार से आता है, उसके मान से कभी नहीं: `21129` और `8.89` दोनों `STRING` घोषित करते हैं, इसलिए जिस दिन series पूर्णांक प्रकाशित करे कुछ नहीं बदलता। पहले batch से पढ़ा टाइप वही विफलता है जिसे यह नियम रोकने के लिए है।

**और `null` कुछ तय नहीं करता — लेकिन पंक्ति में बना रहता है।** ये दो अलग सवाल हैं, और दोनों को एक जवाब देने की कीमत v0.77.0 को चुकानी पड़ी।

Null का कोई आकार नहीं होता, इसलिए **स्कीमा** के पास पढ़ने को कुछ नहीं: जो फ़ील्ड `null` आता है वह कॉलम घोषित नहीं करता, और कॉलम पहले उस रिकॉर्ड से प्रकट होता है जो कोई मान ढोता है, उस मान के आकार के साथ। उलटा, null जवाब के अभाव में `STRING` बन जाता, और उसके बाद आने वाली पहली सूची गंतव्य से पूरा batch मना करवाती — `STRING` से `JSON` की कोई migration उसे पलटने को नहीं।

**पंक्ति** वह ढोती है जो producer ने भेजा, null समेत। उसे ऐसा करना ही है: `Target.Schema` में आपका *घोषित* कॉलम जो खाली आया, पंक्ति में होना चाहिए, वरना घोषणा को उससे मिलाने वाली जाँच ऐसी श्रृंखला पढ़ती है जिसने उसे बनाना बंद कर दिया है — और लोड मना कर देती है। v0.77.0 ने यही किया, और 0.78.0 ने उसे पलटा।

बाकी गंतव्य निपटाता है: batch के हर रिकॉर्ड में null फ़ील्ड, तालिका में बिना कॉलम के, कहीं नहीं लिखा जाता और कुछ मना नहीं करता। Null और अनुपस्थित फ़ील्ड एक ही `NULL` उतरते हैं, इसलिए कुछ नहीं खोता।

**जो फ़ील्ड आकार बदलता है वह मना होता है, सोखा नहीं जाता।** अगर `valor` scalar आया और टेक्स्ट कॉलम बना, तो बाद में आने वाला ऑब्जेक्ट प्रकार का बदलाव है और batch रुक जाता है — संदेश में कॉलम की अपनी टिप्पणी के साथ, ताकि आप देख सकें कि किसी ने उसे घोषित नहीं किया। कॉलम का नाम बदलें, या खुद migrate करें।

**BigQuery पर, एक नाम की दो वर्तनी एक कॉलम हैं।** BigQuery कॉलम का केस मिला देता है: जिस batch में एक रिकॉर्ड में `nationalID` और दूसरे में `nationalId` है वह दो नहीं, **एक** कॉलम घोषित करता है, और जो वर्तनी जीतती है वह क्रमबद्ध क्रम में पहली है — `nationalID`, क्योंकि `D` `d` से पहले आता है। पहले पहुँचने वाली नहीं: दोबारा चलाया गया वही batch अलग वर्तनी वाला कॉलम नहीं बना सकता। जिस तालिका का कॉलम `createdAt` है उसके विरुद्ध `createdat` घोषित करने वाली घोषणा भी वही कॉलम है, इसलिए कुछ जुड़ता नहीं और कुछ मना नहीं होता।

**जो एक रिकॉर्ड दोनों वर्तनी ढोता है वह दोनों का नाम बताकर मना होता है।** गंतव्य में एक कॉलम है और दोनों मानों में से एक चुपचाप जीतता, इसलिए लोड रुकता है: `Transform` में एक का नाम बदलें, या दोनों को एक फ़ील्ड पर map करें। दो *रिकॉर्ड* जो हर एक एक वर्तनी ढोते हैं ठीक हैं — यह सामान्य CDC ट्रैफ़िक है, और यह नियम उसे उतारने के लिए ही है।

**कुछ migrate नहीं होता।** जिस तालिका में पहले से दो कॉलम हैं जो सिर्फ़ केस में भिन्न हैं वह दोनों रखती है; कोई कॉलम नाम नहीं बदलता और कोई नहीं हटता।

**Postgres और MySQL में केस नहीं मिलता।** वहाँ quoted identifier अलग होता है, इसलिए दो वर्तनी दो कॉलम हैं — जानबूझकर, और BigQuery की ओर इंगित वही fetcher उन्हें एक में उतारेगा।

**batch से बना हर कॉलम तालिका में खुद बताता है।** Postgres और MySQL पर टिप्पणी, BigQuery पर फ़ील्ड का विवरण, तारीख़ के साथ:

```
brevis: added from a batch on 2026-09-29; the type is the landing rule
(scalar text, object and array json), not a decision
```

छह महीने बाद "यह कॉलम कब आया, और किसने तय किया" का जवाब देने वाली यही अकेली चीज़ बचती है। तब तक लॉग पंक्ति घूमकर मिट चुकी होती है।

**यह मोड नहीं है।** `Target.Schema` अब भी आप ही हैं जो घोषित करते हैं कि तालिका क्या है — यह आपको पहले से मौजूद लेआउट देता है, टाइप करने की जगह। इसे इस्तेमाल करने से SDK में कुछ अलग व्यवहार नहीं करता, और जो pipeline अपनी स्कीमा घोषित करती है वह अप्रभावित है।

**तालिका का नाम एक बार लिखें।** वह `Landing` को जाता है, क्योंकि वह id का दूसरा slot है, और गंतव्य को, क्योंकि पंक्तियाँ वहाँ जाती हैं। **कुछ नहीं जाँचता कि दोनों सहमत हैं**: `Writer` अपनी तालिका जानता है लेकिन उसे सिर्फ़ `Describe` से उजागर करता है, जो लॉग और त्रुटियों का नाम है, और उस पर बनी id हर पहले से लिखी id को लॉग string से बाँध देती। दोनों को बेमेल कर दें तो पंक्तियाँ सही उतरती हैं, उस तालिका के लिए बनी ids के साथ जिसमें किसी ने लिखा नहीं। वे ठीक दिखेंगी। वे gateway की ids से मेल नहीं खाएँगी।

**तीन कॉलम `NULL` हैं, और यही ईमानदार जवाब है।** `brevis_stream` और `brevis_gateway` उन चीज़ों के नाम हैं जो pipeline के पास नहीं हैं — `brevis_gateway IS NULL` ही pipeline की पंक्ति को gateway की से अलग पहचानने का तरीका है। `brevis_received_bytes` भी `NULL` है: gateway का तार पर आए बाइट गिनता है, envelope समेत, और रिकॉर्ड का अपना JSON करीब 30% छोटा है। दो अर्थ वाला एक कॉलम दोनों रास्तों की पंक्तियों पर योग को इस हिसाब से गलत कर देता कि हिस्सा किससे आया। `length(data)` pipeline वाला संस्करण ठीक बताता है।

**Merge के लिए `DedupKey` चाहिए, और उसके बिना merge है ही नहीं।** हर driver `ingestion_id` पर मिलान करता है जब तक कहा न जाए, और इस लेआउट का पहचान कॉलम `brevis_ingestion_id` है:

```go
Target{
    To:       postgres.Table{DSN: dsn, Name: table, CreateTable: true},
    Schema:   sdk.LandingSchema(sdk.LandingOptions{UniqueID: true, Keyed: true}),
    Dedup:    sdk.DedupMerge,
    DedupKey: sdk.LandingColumnID,
}
```

`UniqueID` वह है जो id पर UNIQUE constraint लगाता है, और Postgres व MySQL दोनों उसके बिना merge से मना करते हैं — `ON CONFLICT` के पास मिलाने को कुछ नहीं होता। ऐसी `DedupKey` जो आपकी `Schema` घोषित नहीं करती कुछ चलने से पहले मना होती है, क्योंकि तालिका में न होने वाले कॉलम पर merge कुछ से मेल नहीं खाता, और कुछ से मेल न खाने वाला merge ठीक वैसा दिखता है जैसे वह उन सबसे मेल खा रहा हो जिनसे खाना चाहिए।

**प्रति गंतव्य, और आज MySQL नहीं कर सकता।** BigQuery में unique constraints नहीं हैं और वह घोषणा को सीधे मना करता है। MySQL का `string` `LONGTEXT` है, और MySQL लंबाई के बिना `LONGTEXT` पर key नहीं बनाता — इसलिए merge करने वाली लैंडिंग तालिका वहाँ *बन* ही नहीं सकती:

```
Error 1170 (42000): BLOB/TEXT column 'brevis_ingestion_id' used in key
specification without a key length
```

Postgres चलता है, क्योंकि उसका `string` `TEXT` है। MySQL पर `DedupNone` के साथ उतारें और मौजूदा संस्करण आगे की ओर resolve करें, या तालिका खुद `CreateSQL` और नाप वाले id कॉलम के साथ बनाएँ।

**Id सामग्री-पता वाली है।** एक ही रिकॉर्ड किसी भी रास्ते से वही id बनाता है, और बदला हुआ रिकॉर्ड नई — जिससे दोबारा चलाना डुप्लिकेट के बजाय no-op बनता है।

## संदर्भ

- [pkg.go.dev](https://pkg.go.dev/github.com/AreteAcademy/brevis/sdk) — पूरी API
- [`examples/`](https://github.com/AreteAcademy/brevis/tree/master/examples) — बारह चलाने योग्य उदाहरण
- [`CHANGELOG.md`](https://github.com/AreteAcademy/brevis/blob/master/CHANGELOG.md) — संस्करण-दर-संस्करण इतिहास
