---
title: White label
description: एक YAML फ़ाइल से इंटरफ़ेस का शीर्षक, ध्येय-वाक्य, लोगो और पैलेट बदलना।
group: संचालन
order: 17
slug: white-label
---

इंटरफ़ेस को एक फ़ाइल से अनुकूलित किया जा सकता है। रंग CSS वेरिएबल को **runtime पर** अधिलेखित करते हैं, इसलिए
किसी ग्राहक की थीम बदलने से कुछ दोबारा compile नहीं होता।

## फ़ाइल

`brand.example.yaml` को root पर `brand.yaml` में कॉपी करें, या `BREVIS_BRAND_FILE` को उस पर
इंगित करें:

```yaml
titulo: Acme Data
subtitulo: Data platform

frase: |
  Measure before deciding.
  Decide before scaling.

cores:
  fundo:    "#f4efe4"
  tinta:    "#21180f"
  destaque: "#aa8450"
```

**हर फ़ील्ड वैकल्पिक है।** जो आप छोड़ दें वह डिफ़ॉल्ट रखता है, इसलिए सिर्फ़ `titulo:` वाली फ़ाइल
भी मान्य है।

`frase` की पंक्ति-विराम सुरक्षित रहती है: वह साइडबार में वाक्य की लय तय करती है।

## लोगो

```yaml
logo: https://example.com/mark.svg
```

URL (`https://` या `http://`) या `/` से शुरू होता आंतरिक path स्वीकार करता है। न हो, तो अंतर्निहित
चिह्न इस्तेमाल होता है, जो थीम के रंग का अनुसरण करता है।

:::warning कहीं और होस्ट किया लोगो उस होस्ट पर निर्भर है
अगर वह बंद हो जाए, या cluster के पास इंटरनेट egress न हो, तो image लोड नहीं होती। इंटरफ़ेस का बाकी हिस्सा चलता रहता
है, लेकिन चुनते समय इसे तौलें — बाकी UI, फ़ॉन्ट और bundles समेत, बाइनरी से ही परोसा जाता है, ठीक इसीलिए कि नेटवर्क
पर निर्भर न रहना पड़े।
:::

## Validate करना

गलत hex मान तभी सामने आता जब container उठता, और संदेश pod के लॉग से आता — फ़ाइल बदलने वाले से दूर। इसलिए एक
सबकमांड है:

```bash
brevis marca brand.yaml
```

```
  ok    Acme Data · Data platform
        logo      /assets/logo.svg  (simbolo embutido)
        destaque  #aa8450
        Powered by Brevis
```

इसे इंस्टॉलेशन के CI में बुलाएँ और त्रुटि pull request में लौट आती है।

:::note अनुपस्थिति के अलग-अलग मतलब हैं
`serve` में, फ़ाइल का न होना "डिफ़ॉल्ट पहचान इस्तेमाल करो" है — और API उठ जाती है। `marca` में, फ़ाइल का न
होना **त्रुटि** है: जिसने path को validate करने को कहा, वह यह सुनने की उम्मीद करता है कि वह मौजूद नहीं है।
:::

## Boot पर व्यवहार

सामग्री की त्रुटि — अमान्य रंग, टूटी YAML — इंटरफ़ेस को उठने से **नहीं रोकती**। प्रोसेस चेतावनी लॉग करती है
और डिफ़ॉल्ट थीम इस्तेमाल करती है। एक रंग की वजह से API को गिरा देना, उसे डिफ़ॉल्ट थीम के साथ परोसने से बुरा होता।

```
level=WARN msg="identidade visual ignorada" arquivo=brand.yaml erro="cor invalida: #gggggg"
```

## क्या अनुकूलित नहीं हो सकता

फ़ुटर की **"Powered by Brevis"** पंक्ति कॉन्फ़िगरेशन से नहीं आती — वह कोड से आती है।

## Kubernetes पर

फ़ाइल को ConfigMap से माउंट करें:

```yaml
volumes:
  - name: brand
    configMap: {name: brevis-brand}
containers:
  - name: api
    volumeMounts:
      - {name: brand, mountPath: /etc/brevis}
    env:
      - name: BREVIS_BRAND_FILE
        value: /etc/brevis/brand.yaml
```
