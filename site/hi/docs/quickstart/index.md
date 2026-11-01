# क्विकस्टार्ट

> शून्य से इंटरफ़ेस में चलते DAG तक, चार चरणों में।

*https://brevis.sh/hi/docs/quickstart/ · brevis.sh docs (hi)*

---

यह गाइड brevis.sh को खड़ा करती है, एक workflow प्रकाशित करती है और उसे चलाती है।
अंत में आप हर step की लाइव स्थिति के साथ ग्राफ़ देखते हैं।

आवश्यकता: एक पहुँच में आने वाला PostgreSQL और `BREVIS_DATABASE_URL` वेरिएबल।

## 1. स्कीमा लागू करें

```bash
export BREVIS_DATABASE_URL='postgres://brevis:brevis@localhost:5432/brevis?sslmode=disable'
brevis migrate up
```

## 2. एक workflow लिखें

`hello.yaml` बनाएँ। यह उदाहरण सिर्फ़ shell इस्तेमाल करता है, इसलिए कहीं भी चलता है:

```yaml
name: hello
type: dag
tags: [example]

steps:
  - id: prepare
    run: sh -c 'echo preparing; sleep 1'

  # The next two depend on the same step, so they run IN PARALLEL.
  - id: extract
    run: sh -c 'sleep 2; echo 42 extracted'
    depends_on: [prepare]

  - id: validate
    run: sh -c 'sleep 1; echo validated'
    depends_on: [prepare]

  - id: publish
    run: echo published
    depends_on: [extract, validate]
```

प्रकाशित करने से पहले इसे जाँचें — यह डेटाबेस को नहीं छूता:

```bash
brevis validate hello.yaml
```

```text
  ok    hello                        dag  4 steps, 4 dependencies  (manual)
```

## 3. इसे अभी चलाएँ, कतार के बिना

ग्राफ़ को काम करते देखने का सबसे छोटा रास्ता:

```bash
brevis run hello.yaml
```

```text
workflow hello (dag, 4 steps) in .
  ▶ prepare
    prepare | preparing
  ✓ prepare
  ▶ extract
  ▶ validate
    validate | validated
  ✓ validate
    extract | 42 extracted
  ✓ extract
  ▶ publish
    publish | published
  ✓ publish

workflow hello finished
```

`extract` और `validate` साथ शुरू होते हैं क्योंकि वे एक ही step पर निर्भर हैं।
Runner ग्राफ़ पर **स्तरों के हिसाब से** चलता है: एक स्तर के भीतर सब कुछ समानांतर
चलता है, और अगला स्तर तभी शुरू होता है जब पिछला पूरी तरह बंद हो जाए।

:::note `run` स्वभाव से स्थानीय है
`brevis run` सिर्फ़ `BREVIS_ENV=local` के साथ काम करता है (खाली को local माना जाता है)
और steps को उसी इंस्टेंस पर प्रोसेस की तरह चलाता है — उन्हें भी जो `image:` घोषित करते
हैं। Pods `scheduler` बनाता है।
:::

## 4. प्रकाशित करें और संचालित करें

ताकि workflow डेटाबेस में मौजूद हो, इंटरफ़ेस में दिखे और अपने शेड्यूल का पालन करे:

```bash
brevis publish hello.yaml
```

```text
  published  hello                      (manual)
```

दो टर्मिनल में इंटरफ़ेस और शेड्यूलर चलाएँ:

```bash
brevis serve
```

```bash
brevis scheduler --interval 5s --concurrency 4
```

`http://localhost:8080` खोलें, workflow की सूची में `hello` ढूँढें और ▶ पर क्लिक
करें। ग्राफ़ में हर step की स्थिति लाइव बदलती दिखती है।

## क्या हुआ

| | |
|---|---|
| `migrate up` | तालिकाएँ बनाईं |
| `validate` | डेटाबेस को छुए बिना YAML जाँचा |
| `run` | उसी इंस्टेंस पर चलाया, कतार के बिना |
| `publish` | workflow और उसका शेड्यूल सहेजा |
| `serve` | API और इंटरफ़ेस खड़े किए |
| `scheduler` | शेड्यूल को run में बदला और कतार खाली की |

## अगले कदम

- [Workflows](/hi/docs/workflows/index.md) — पूरा YAML फ़ॉर्मेट
- [पैरामीटर](/hi/docs/parameters/index.md) — दो run के बीच क्या बदलता है
- [हर step के लिए एक pod](/hi/docs/pod-per-step/index.md) — यह Kubernetes कैसे बनता है
