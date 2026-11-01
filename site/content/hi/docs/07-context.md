---
title: steps के बीच संदर्भ
description: एक step अगले को क्या बताता है — दो environment वेरिएबल और एक फ़ाइल।
group: अवधारणाएँ
order: 7
slug: context
---

एक step मान प्रकाशित करता है; उस पर निर्भर steps उन्हें पढ़ते हैं। यह फ़ीचर बस इतना ही
करता है।

```yaml
steps:
  - id: extract
    run: python fetch.py

  - id: transform
    run: python transform.py
    depends_on: [extract]
```

```python
# extract
from brevis import context
context.set(bucket="s3://landing/2026-09-07", rows=48213)
```

```python
# transform
from brevis import context
bucket = context.get("extract.bucket")
```

Key हमेशा `<step id>.<name>` होती है। Step वही पढ़ता है जो **उसकी अपनी निर्भरताओं**
ने प्रकाशित किया, और कुछ नहीं।

:::note यह क्या नहीं है
यह message queue नहीं है, run के बीच की स्थिति नहीं है, और डेटा रखने की जगह नहीं है। यह वह
पर्ची है जो एक step अगले के लिए छोड़ता है — एक path, एक watermark, एक गिनती।
:::

## कोई भी भाषा, किसी लाइब्रेरी की ज़रूरत नहीं

अनुबंध **दो environment वेरिएबल और एक फ़ाइल** है, इसलिए बिना SDK वाला step भी इसमें हिस्सा लेता है:

```bash
window=$(jq -r '.extract.watermark' <<< "$BREVIS_INPUT")
./publish --until "$window"
jq -n --arg n "$(date -Is)" '{published_at: $n}' > "$BREVIS_OUTPUT"
```

| | |
|---|---|
| `BREVIS_INPUT` | `{"<step id>": {…}}` — इस step की निर्भरताओं ने जो प्रकाशित किया |
| `BREVIS_OUTPUT` | उस path का नाम जिस पर **एक** JSON ऑब्जेक्ट लिखना है |

Kubernetes के तहत वह path `/dev/termination-log` है, जिसे engine exit code पाने के लिए पहले से पढ़ता है।
इसलिए **कोई API कॉल, कोई token और कोई port नहीं** — और इसके लिए लाइब्रेरी एक JSON parse और एक फ़ाइल write है।

| भाषा | |
|---|---|
| Python | [`pip install brevis`](/docs/python/) — इसमें `brevis.run` भी है, [auto पैरामीटर](/docs/parameters/) के साथ |
| Go | `sdk/context`, SDK module का हिस्सा, किसी पर निर्भर नहीं |
| बाकी कुछ भी | ऊपर के दो वेरिएबल |

## अलगाव

कोई step दूसरे के प्रकाशित को अधिलेखित नहीं कर सकता। Keys पर लिखने वाले की id का उपसर्ग लगता है, और वह उपसर्ग
engine तय करता है — step नहीं। `bucket` प्रकाशित करने वाले दो steps `extract.bucket` और `load.bucket` बनाते हैं,
और कोई गायब नहीं होता।

## 4096 बाइट की सीमा

Step जो प्रकाशित करता है वह **4096 बाइट** में समाना चाहिए। सीमा मनमानी नहीं है: Kubernetes के तहत transport
`/dev/termination-log` है, और kubelet उतना ही स्वीकार करता है।

सीमा लाँघना **step की त्रुटि** है, और संदेश बताता है कि कितने बाइट लिखे गए। विकल्प छाँटना होता — और छँटा हुआ
JSON अमान्य JSON है, जिसे अगला step खोजता है और जिसके पास यह जानने का कोई तरीका नहीं कि दोष उसका अपना नहीं है।

अगर कोई मान नहीं समाता, तो वह संदर्भ नहीं है: उसे bucket में लिखें और path प्रकाशित करें।

## Retries और फिर शुरू किए गए run

नए प्रयास पर step दोबारा प्रकाशित करता है और पिछला मान बदल जाता है। जो step पहले ही सफल हो चुका है वह न दोबारा
चलता है, न दोबारा प्रकाशित करता है — उसने जो कहा था वह उन पर टिका रहता है जो उस पर निर्भर हैं।

## Step को हाथ से चलाना

दो वेरिएबल ही पूरा अनुबंध हैं, इसलिए किसी run को दोहराना उन्हें export करना भर है:

```bash
export BREVIS_INPUT='{"extract":{"bucket":"s3://landing/2026-09-07"}}'
export BREVIS_OUTPUT=/tmp/out.json
python transform.py
cat /tmp/out.json
```

## अगले कदम

- [पैरामीटर](/docs/parameters/) — run के बीच क्या बदलता है, और स्वचालित वाले
- [Step runtime](/docs/runtime/) — step अपने चलने की जगह के बारे में क्या घोषित करता है
- [Python](/docs/python/) — क्लाइंट लाइब्रेरी, और अन्य भाषाएँ
