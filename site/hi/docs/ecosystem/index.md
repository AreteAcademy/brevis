# इकोसिस्टम

> brevis.sh के चार हिस्से, हर एक क्या करता है, और डेटा कहाँ से होकर गुज़रता है।

*https://brevis.sh/hi/docs/ecosystem/ · brevis.sh docs (hi)*

---

Core शेड्यूल करता है, कतार में रखता है और निगरानी करता है — और डेटा को कभी नहीं छूता। डेटा को छूते हैं ये तीन: SDK, जो डेटा लाता है; Gateway, जो डेटा ग्रहण करता है; और SQL, जो उस डेटा को मॉडल करता है जो पहले ही अपनी जगह पहुँच चुका है।

## चार हिस्से

| हिस्सा | क्या करता है | कैसे मिलता है | गाइड |
|---|---|---|---|
| **Core** | शेड्यूल करता है, कतार में रखता है और हर step के लिए एक pod शुरू करता है। डेटा को कभी नहीं छूता। | `areteacademy/brevis` | [Core](/hi/docs/core/index.md) |
| **SDK** | एक Go लाइब्रेरी जो आपके step के भीतर डेटा निकालती है, उसे ढालती है और लोड करती है। | `go get github.com/AreteAcademy/brevis/sdk` | [Go SDK](/hi/docs/sdk/index.md) |
| **Gateway** | HTTP POST लेता है, 202 लौटाता है और बैच में लिखता है। इसका अपना कोई डेटाबेस नहीं। | `areteacademy/brevis-gateway` | [Ingestion](/hi/docs/ingestion/index.md) |
| **SQL** | सादी .sql फ़ाइलें निर्भरता के क्रम में तालिकाएँ और view बन जाती हैं। | `areteacademy/brevis-sql` | [SQL](/hi/docs/sql/index.md) |

## कंट्रोल प्लेन, डेटा प्लेन

**कंट्रोल प्लेन** Core है: वह तय करता है कि हर step कब चलेगा, कतार और स्थिति को
Postgres में रखता है और हर step के लिए एक pod शुरू करता है। डेटा कभी उसके
रास्ते से नहीं गुज़रता।

**डेटा प्लेन** बाकी तीन हिस्से हैं। डेटा किसी API, डेटाबेस या फ़ाइलों से आता है और
दो में से एक रास्ते से अपने गंतव्य तक पहुँचता है: **SDK उसे लाता है**, आपके किसी
step के भीतर, या वह webhook और ऐप्स से HTTP के ज़रिए **Gateway को भेजा जाता है**।
गंतव्य कोई तालिका, कोई topic या किसी bucket की फ़ाइलें होती हैं। डेटा पहुँच जाने के
बाद **SQL** तालिकाओं को वहीं मॉडल करता है, उसी डेटाबेस में जहाँ डेटा उतरा।

### आसपास के हिस्से

तीन सहायक हिस्से:

- **[Console](/hi/docs/console/index.md)** — Core के ऊपर बना वेब इंटरफ़ेस
- **[agent](/hi/docs/pod-per-step/#where-each-step-runs)** — किसी step को आपके पहले से मौजूद cluster में चलाता है
- **[Python के लिए `brevis`](/hi/docs/python/index.md)** — Python step के लिए run संदर्भ

## सिर्फ़ एक हिस्सा इस्तेमाल करना

हाँ, किया जा सकता है। हर हिस्से का अपना module, संस्करण और image है: SDK एक Go लाइब्रेरी है जो किसी भी प्रोग्राम में चलती है, Gateway एक YAML फ़ाइल से बिना डेटाबेस के अपने आप शुरू हो जाता है, और brevis-sql एक container की तरह चलता है। जब आप चाहें, Core बाकी हिस्सों का ऑर्केस्ट्रेशन करता है — वह उनमें से किसी की पूर्वापेक्षा नहीं है।

## वही पंक्ति

SDK जो ऑर्डर लाता है और Gateway पर जो ऑर्डर आता है, दोनों एक ही तालिका में उतरते हैं, एक ही brevis_ingestion_id के साथ। दोनों में से चुनना deployment का फ़ैसला बन जाता है, स्कीमा का नहीं।

SDK के ज़रिए, `sdk.Landing` के साथ (विवरण [लैंडिंग लेआउट](/hi/docs/sdk/#the-landing-layout) में):

```go
sdk.Execute(ctx, &sdk.Pipeline{
	Source:    sdk.Source{From: from.Files{Path: "order.json"}},
	Transform: []sdk.Transformer{sdk.Landing("app_orders", sdk.LandingKey("id"))},
	Target: sdk.Target{
		To:     postgres.Table{DSN: dsn, Name: "app_orders"},
		Schema: sdk.LandingSchema(sdk.LandingOptions{}),
	},
}, nil)
```

Gateway के ज़रिए, `/v1/tables` पर एक POST (विवरण [एक route, N तालिकाएँ](/hi/docs/ingestion/#one-route-n-tables) में):

```bash
curl -X POST localhost:8080/v1/tables -H 'Authorization: Bearer dev-key' \
    -d '{"table_name":"app_orders","unique_key":"id",
         "data":{"id":"A-1","customer":"acme.example.com","total":150}}'
```

जवाब:

```text
{"accepted":1,"rejected":null}  202
```

`app_orders` में जो उतरा:

| कॉलम | SDK के ज़रिए | Gateway के ज़रिए |
|---|---|---|
| `brevis_ingestion_id` | `af5e2a31-fa3c-576c-a0c8-a91517e51149` | `af5e2a31-fa3c-576c-a0c8-a91517e51149` |
| `brevis_record_key` | `A-1` | `A-1` |
| `brevis_operation` | `INSERT` | `INSERT` |
| `brevis_gateway` | `NULL` | `acme_gateway` |
| `data` | `{"id": "A-1", "total": 150, "customer": "acme.example.com"}` | `{"id": "A-1", "total": 150, "customer": "acme.example.com"}` |

असली आउटपुट: दोनों रास्ते एक ही स्थानीय Postgres पर, प्रकाशित images और SDK के साथ चलाए गए। फ़र्क बस इतना है कि लिखा किसने — SDK वाली पंक्ति में brevis_gateway खाली है।
