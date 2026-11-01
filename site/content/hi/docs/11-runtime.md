---
title: Step runtime
description: Step अपनी भाषा और जिन टूल को चलाता है उनके बारे में क्या घोषित करता है — और आपको इसकी शायद ही ज़रूरत क्यों पड़ती है।
group: संदर्भ
order: 11
slug: runtime
---

ग्राफ़ हर step पर एक chip बनाता है जो उसकी भाषा और उसके चलाए टूल बताता है:

```
┌──────────────────────────────────────┐
│ ● transform            SDK v0.57.0   │
│   dbt build --select gold            │
│   ⬤ Python  ▢ dbt                    │
│   2m14s · attempt 1                  │
└──────────────────────────────────────┘
```

**ज़्यादातर समय आप कुछ नहीं लिखते।** Engine `run:` और `image:` पढ़कर खुद निकाल लेता है।

## इसे कब घोषित करें

दो फ़ील्ड, दोनों वैकल्पिक, दोनों प्रति step:

```yaml
steps:
  - id: fetch
    run: /opt/brevis/bin/fetch-weather
    runtime: go            # a compiled binary; the path says nothing

  - id: transform
    run: dbt build --select gold+
    tools: [dbt]           # what it drives, not what it is
```

| फ़ील्ड | |
|---|---|
| `runtime` | step की भाषा: `go`, `python`, `node`, `shell`… |
| `tools` | कमांड जिन टूल को चलाती है: `dbt`, `dlt`, `spark`… |

मोटा नियम: **तब घोषित करें जब अनुमान गलत निकले।** जिस `run:` का इशारा किसी compile की हुई बाइनरी की ओर है,
उसके पास यह बताने का तरीका नहीं कि वह Go है; जो wrapper भीतर से `dbt` बुलाता है, उसके पास यह बताने का तरीका नहीं कि
वह dbt चलाता है।

## बंद शब्दावली क्यों

शब्दावली से बाहर की id **प्रकाशित करते समय ही मना** कर दी जाती है, और बताया जाता है कि क्या मान्य है।
विकल्प तीन दिन बाद स्क्रीन पर खाली दिखता chip है, जिसे ट्रेस करने के लिए कुछ नहीं होता।

```
workflow "daily": step "transform": `runtime: pyton` is not valid
  (valid: go, python, node, shell, …)
```

## Chip किसलिए है

यह सजावट नहीं है। जब रात के 3 बजे कोई run विफल होता है, तो पहला सवाल होता है "यह step क्या चलाता है?", और
जवाब आम तौर पर किसी दूसरी रिपॉज़िटरी में होता है। Chip उसे ग्राफ़ पर ही रख देता है — step ने जो SDK संस्करण इस्तेमाल किया
उसके साथ, जो "आज टूटा" को "मंगलवार के बदलाव से टूटा" से अलग करता है।

## अगले कदम

- [Workflows](/docs/workflows/) — step का हर फ़ील्ड
- [हर step के लिए एक pod](/docs/pod-per-step/) — step असल में कहाँ चलता है
- [ऑब्ज़र्वेबिलिटी](/docs/observability/) — इसके साथ चलने वाले मेट्रिक्स
