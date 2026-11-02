# Kubernetes

> Engine को कैसे deploy करें, किन अनुमतियों के साथ, और उसके बाद क्या जाँचें।

*https://brevis.sh/hi/docs/kubernetes/ · brevis.sh docs (hi)*

---

एक आम इंस्टॉलेशन में उसी बाइनरी के **दो Deployments** होते हैं — API और शेड्यूलर — और एक Postgres।

## Helm से इंस्टॉल करें

Chart रिपॉज़िटरी में `deployments/helm/brevis` पर है। चार मान ज़रूरी हैं और बाकी सबके पास काम करने लायक
डिफ़ॉल्ट है:

```bash
helm install brevis ./deployments/helm/brevis \
  --namespace data --create-namespace \
  --set database.url="postgres://brevis:pw@postgres/brevis?sslmode=require" \
  --set auth.user=admin \
  --set auth.passwordHash="$(brevis hash)" \
  --set auth.secret="$(openssl rand -base64 48)"
```

पूरा इंस्टॉल बस इतना है: migrations पहले hook के रूप में चलते हैं, दोनों Deployments हर एक की सही image के
साथ उठते हैं, और शेड्यूलर को ऐसा ServiceAccount मिलता है जो pods बना सकता है जबकि task pods को ऐसा जो कुछ
नहीं कर सकता।

इंटरफ़ेस तक पहुँचने के लिए:

```bash
helm upgrade brevis ./deployments/helm/brevis --reuse-values \
  --set api.ingress.enabled=true \
  --set api.ingress.host=brevis.example.com \
  --set api.ingress.tls.enabled=true \
  --set api.ingress.tls.secretName=brevis-tls
```

Production में कनेक्शन स्ट्रिंग को release से बाहर रखें: उसे Secret में रखें और
`--set database.existingSecret=brevis-db` दें। Inline देने पर वह release के सहेजे manifest में पहुँच जाती है,
जिसे उस namespace में Secrets पढ़ सकने वाला कोई भी पढ़ सकता है।

`helm show values ./deployments/helm/brevis` हर मान को उसके कारण के साथ सूचीबद्ध करता है।

### Chart क्या मना करता है

जो कुछ वह सुरक्षित नहीं बना सकता, वह render के समय ही विफल होता है, ऐसे संदेश के साथ जो बताता है कि क्या टूटता —
रात के 3 बजे, डुप्लिकेट पंक्तियाँ बना चुके किसी run में नहीं:

| मना किया जाता है | क्योंकि |
|---|---|
| `scheduler.replicas` बिल्कुल भी | दो replicas वही slots बना देंगे। `FOR UPDATE SKIP LOCKED` उन्हें कतार का एक ही आइटम दावा करने से रोकता है; उन्हें एक ही शेड्यूल्ड run बनाने से कुछ नहीं रोकता, और `dbt build` एक ही विंडो पर दो बार चलता, बिना कुछ विफल हुए |
| `local` के बाहर क्रेडेंशियल का न होना | Engine बिना क्रेडेंशियल के boot से मना करता है, इसलिए chart आपको CrashLoopBackOff थमा रहा होता |
| 32 बाइट से कम का `auth.secret` | वह session cookie पर हस्ताक्षर करता है, और engine वही न्यूनतम लागू करता है |
| बिना `host` वाला Ingress | वह controller तक पहुँचने वाले हर अनुरोध से मेल खाता — साझा cluster में, किसी और के hostname का जवाब देता |
| बिना webhook के `alerts.enabled` | Pod outbox खाली करता और कहीं डिलीवर न करता, जो न चलने से बुरा है: तब विफलता घोषित हुई-सी लगती है |
| ऐसा मान जिसे chart परिभाषित नहीं करता | `--set` में टाइपो वरना चुपचाप कुछ नहीं करती |

`.github/scripts/helm-check.sh` यह सब render हुए आउटपुट पर जाँचता है, साथ में ऊपर की अपरिवर्तनीयताएँ भी — एक
शेड्यूलर, `pods/log` मौजूद, migrations पहले, metrics port Service से बाहर।

## Helm के बिना

सात manifests `deployments/kubernetes/` में हैं, namespace और image tag बदलने के बाद `kubectl apply -f` के लिए
तैयार:

```bash
kubectl apply -f deployments/kubernetes/rbac.yaml
kubectl apply -f deployments/kubernetes/api.yaml
kubectl apply -f deployments/kubernetes/scheduler.yaml
```

उनमें वही फ़ैसले हैं जो chart में हैं, तर्क टिप्पणियों में, और इनके अलावा `alert.yaml`, `report.yaml` और
`publish-job.yaml`। नीचे के खंड बताते हैं कि हर एक में क्या है।

## दो भूमिकाएँ

| भूमिका | कमांड | image | replicas |
|---|---|---|---|
| API + इंटरफ़ेस | `serve` | `:0.11.2` (distroless) | जितने चाहें |
| शेड्यूलर + कतार | `scheduler` | `:0.11.2-worker` (alpine) | **एक** |

API distroless है क्योंकि वह सिर्फ़ HTTP परोसती है: कुछ चलाती नहीं, इसलिए उसे shell नहीं चाहिए। Worker alpine है
क्योंकि `run:` steps को shell चाहिए।

:::warning एक शेड्यूलर replica
एक ही schedule बनाने वाली दो प्रोसेस डुप्लिकेट run बनाती हैं। उपलब्धता चाहिए तो `strategy: Recreate` के साथ
`replicas: 1` इस्तेमाल करें।
:::

## Migrations

उन्हें deploy से पहले Job के रूप में चलाएँ, `serve` के boot पर कभी नहीं:

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: brevis-migrate
spec:
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: migrate
          image: brevissh/brevis:0.40.0
          args: ["migrate", "up"]
          envFrom:
            - secretRef: {name: brevis-db}
```

## API Deployment

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: brevis-api
spec:
  replicas: 2
  selector:
    matchLabels: {app: brevis-api}
  template:
    metadata:
      labels: {app: brevis-api}
    spec:
      serviceAccountName: brevis
      containers:
        - name: api
          image: brevissh/brevis:0.40.0
          args: ["serve"]
          ports: [{containerPort: 8080}]
          envFrom:
            - secretRef: {name: brevis-db}
            - secretRef: {name: brevis-auth}
          env:
            - name: BREVIS_ENV
              value: production
          livenessProbe:
            httpGet: {path: /health, port: 8080}
          readinessProbe:
            httpGet: {path: /ready, port: 8080}
          resources:
            requests: {cpu: 100m, memory: 128Mi}
            limits: {memory: 256Mi}
```

:::note दोनों probes अलग क्यों हैं
`/health` डेटाबेस से query **नहीं** करता; `/ready` करता है, और विफल हुई निर्भरता का नाम बताता है। अगर liveness
Postgres पर निर्भर होता, तो डेटाबेस की हल्की डगमगाहट Kubernetes से API pods को load balancer से हटाने भर के बजाय
**मरवा** देती — और रिकवरी ठीक तब धीमी होती जब सिस्टम पहले से दबाव में है।
:::

## शेड्यूलर Deployment

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: brevis-scheduler
spec:
  replicas: 1
  strategy: {type: Recreate}
  selector:
    matchLabels: {app: brevis-scheduler}
  template:
    metadata:
      labels: {app: brevis-scheduler}
    spec:
      serviceAccountName: brevis
      containers:
        - name: scheduler
          image: brevissh/brevis:0.40.0-worker
          args: ["scheduler", "--interval", "10s", "--concurrency", "5", "--max-pods", "10"]
          envFrom:
            - secretRef: {name: brevis-db}
            - secretRef: {name: brevis-auth}
          env:
            - name: BREVIS_ENV
              value: production
            - name: BREVIS_PODS
              value: "on"
            - name: BREVIS_POD_NAMESPACE
              value: data
            - name: BREVIS_POD_SERVICE_ACCOUNT
              value: brevis-runner
            - name: BREVIS_POD_ENV_FROM_SECRETS
              value: bigquery-cred
```

`BREVIS_PODS=on` को cluster **चाहिए**: service account माउंट न हो तो boot चुपचाप स्थानीय execution पर लौटने के बजाय
विफल होता है।

## अनुमतियाँ

शेड्यूलर execution namespace में pods बनाता, देखता और हटाता है। इससे ज़्यादा कुछ नहीं:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: brevis-runner
  namespace: data
rules:
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["create", "get", "list", "watch", "delete"]
  - apiGroups: [""]
    resources: ["pods/log"]
    verbs: ["get"]
```

`pods/log` वह है जो step के आउटपुट को इंटरफ़ेस में फ़ॉलो करने देता है। इसके बिना run चलता है और लॉग खाली रहता है।

## Deploy के बाद

```bash
kubectl -n data get pods -l app=brevis-api
kubectl -n data exec deploy/brevis-api -- brevis version
curl -fsS https://brevis.example.com/ready
```

इंटरफ़ेस से कोई सरल workflow ट्रिगर करें और पुष्टि करें कि step का pod प्रकट होता और गायब होता है:

```bash
kubectl -n data get pods -w
```

## जब कोई step Pending में रहे

यह लगभग हमेशा इन तीन में से एक होता है:

| कारण | पुष्टि कैसे करें |
|---|---|
| toleration के बिना taint | `kubectl describe pod` में `node(s) had untolerated taint` दिखता है |
| बिना node वाला `nodeSelector` | `describe` में `didn't match Pod's node affinity` दिखता है |
| संसाधन उपलब्ध नहीं | `describe` में `Insufficient cpu` या `memory` दिखता है |

विफल step के pod को गायब होते देखने के बजाय जाँचने के लिए:

```bash
BREVIS_POD_KEEP_ON_FAILURE=true
```

Production में इसे बंद रखें — रुके हुए pods quota खाते हैं।

## अगले कदम

- [हर step के लिए एक pod](/hi/docs/pod-per-step/index.md) — execution मॉडल
- [कॉन्फ़िगरेशन](/hi/docs/configuration/index.md) — हर `BREVIS_POD_*` वेरिएबल
- [ऑब्ज़र्वेबिलिटी](/hi/docs/observability/index.md) — क्या scrape करें, और किस प्रोसेस से
