#!/usr/bin/env bash
# The chart renders, and what it renders is correct.
#
# `helm lint` only says the templates parse. What breaks an installation is a
# rendered manifest that Kubernetes ACCEPTS and that is wrong at runtime -- two
# scheduler replicas materialising duplicate runs, a Role without `pods/log` so
# every log stays empty, migrations racing the API instead of preceding it. Each
# of those is asserted below, on the rendered output.
#
# It also checks the refusals, because a validation nobody exercises is a
# validation that stops working the next time somebody edits the file.
set -uo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$root"
CHART=deployments/helm/brevis
HELM_VERSION="${HELM_VERSION:-v3.16.3}"
KUBECONFORM_VERSION="${KUBECONFORM_VERSION:-v0.6.7}"
K8S_VERSION="${K8S_VERSION:-1.29.0}"

os="$(uname -s | tr 'A-Z' 'a-z')"
arch="$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')"
mkdir -p bin

# Both tools are PINNED, for the reason generated-check.sh gives about Tailwind:
# a gate that compares against a tool which changes on its own is not
# reproducible.
if [ ! -x bin/helm ] || ! ./bin/helm version --short 2>/dev/null | grep -q "${HELM_VERSION}"; then
  curl -sSLf "https://get.helm.sh/helm-${HELM_VERSION}-${os}-${arch}.tar.gz" -o bin/helm.tgz || exit 1
  tar xzf bin/helm.tgz -C bin --strip-components=1 "${os}-${arch}/helm" && rm -f bin/helm.tgz
fi
if [ ! -x bin/kubeconform ]; then
  curl -sSLf "https://github.com/yannh/kubeconform/releases/download/${KUBECONFORM_VERSION}/kubeconform-${os}-${arch}.tar.gz" -o bin/kc.tgz || exit 1
  tar xzf bin/kc.tgz -C bin kubeconform && rm -f bin/kc.tgz
fi
HELM=./bin/helm
KUBECONFORM=./bin/kubeconform

MIN=(--set database.url=postgres://u:p@pg/brevis
     --set auth.user=admin
     --set auth.passwordHash='pbkdf2-sha256$x'
     --set auth.secret=0123456789012345678901234567890123456789)

falhas=0
erro() { echo "::error::$*"; falhas=$((falhas + 1)); }

echo "→ helm $($HELM version --short), kubeconform, kubernetes $K8S_VERSION"

$HELM lint "$CHART" "${MIN[@]}" >/dev/null || erro "helm lint failed"

rendered="$(mktemp)"; trap 'rm -f "$rendered" "$full"' EXIT
$HELM template brevis "$CHART" --namespace data "${MIN[@]}" > "$rendered" || {
  erro "the chart does not render with the minimum values"; exit 1; }

full="$(mktemp)"
$HELM template brevis "$CHART" --namespace data "${MIN[@]}" \
  --set api.ingress.enabled=true --set api.ingress.host=brevis.example.com \
  --set api.ingress.tls.enabled=true --set api.ingress.tls.secretName=tls \
  --set alerts.enabled=true --set report.enabled=true \
  --set slack.webhook=https://hooks.slack.com/x --set uiURL=https://brevis.example.com \
  --set brand.enabled=true --set brand.content.title=Acme \
  --set 'scheduler.taskEnvFromSecrets={brevis-task-env}' \
  --set scheduler.taskNodeSelector=kubernetes.io/arch=arm64 > "$full" || {
  erro "the chart does not render with everything enabled"; exit 1; }

for f in "$rendered" "$full"; do
  $KUBECONFORM -strict -kubernetes-version "$K8S_VERSION" "$f" || erro "kubeconform rejected the output"
done

# --- what has to be true of the output -------------------------------------
tem() { grep -q -- "$2" "$1"; }

# One scheduler, and a rollout that does not overlap it. Two replicas would
# materialise the same slots; RollingUpdate would briefly run two.
python3 - "$rendered" <<'PY' || erro "the scheduler is not exactly one replica with strategy Recreate"
import re, sys
doc = open(sys.argv[1]).read()
bloco = [d for d in doc.split('---') if 'component: scheduler' in d and 'kind: Deployment' in d]
assert len(bloco) == 1, "expected exactly one scheduler Deployment"
b = bloco[0]
assert re.search(r'^  replicas: 1$', b, re.M), "scheduler replicas is not 1"
assert 'type: Recreate' in b, "scheduler strategy is not Recreate"
PY

# Without pods/log the run works and every log stays empty, which reads as a
# broken interface rather than a missing permission.
#
# Parsed rather than grepped: the template's own YAML comment mentions
# `pods/log`, and that comment survives into the rendered output -- so a grep
# passed with the rule deleted. A test that cannot fail is worse than none.
python3 - "$rendered" <<'PY' || erro "the scheduler Role's rules are wrong"
import re, sys
doc = open(sys.argv[1]).read()
blocos = [d for d in doc.split('---') if re.search(r'^kind: Role$', d, re.M)]
assert len(blocos) == 1, 'expected exactly one Role, got %d' % len(blocos)
# Strip comments before reading the rules, for the reason above.
corpo = '\n'.join(l for l in blocos[0].split('\n') if not l.strip().startswith('#'))
regras = corpo.split('rules:')[1]
recursos = re.findall(r'resources: \[([^\]]*)\]', regras)
verbos = re.findall(r'verbs: \[([^\]]*)\]', regras)
achatado = ' '.join(recursos)
assert '"pods/log"' in achatado, 'the Role does not grant pods/log: %r' % recursos
assert '"pods"' in achatado, 'the Role does not grant pods: %r' % recursos
todos = ' '.join(verbos)
for proibido in ('"update"', '"patch"', '"*"'):
    assert proibido not in todos, 'the Role grants %s, which the scheduler never needs' % proibido
for preciso in ('"create"', '"get"', '"list"', '"watch"', '"delete"'):
    assert preciso in todos, 'the Role is missing %s' % preciso
PY

# Migrations precede everything: `serve` never changes the schema.
python3 - "$rendered" <<'PY' || erro "the migrate Job is not a pre-install/pre-upgrade hook"
import sys
doc = open(sys.argv[1]).read()
bloco = [d for d in doc.split('---') if 'kind: Job' in d and 'migrate' in d]
assert len(bloco) == 1, "expected exactly one migrate Job"
b = bloco[0]
assert 'helm.sh/hook: pre-install,pre-upgrade' in b, "missing the hook annotation"
assert 'hook-weight: "-5"' in b, "missing the weight that puts it first"
PY

# The right image per role: the API executes nothing (distroless, no shell) and
# the worker image is the one with a shell.
python3 - "$rendered" <<'PY' || erro "the images per role are wrong"
import sys
doc = open(sys.argv[1]).read()
for comp, sufixo in (("api", False), ("scheduler", True)):
    b = [d for d in doc.split('---') if f'component: {comp}' in d and 'kind: Deployment' in d][0]
    linha = [l for l in b.split('\n') if 'image:' in l][0]
    if sufixo and not linha.strip().endswith('-worker'):
        raise SystemExit(f"{comp} should use the -worker image: {linha.strip()}")
    if not sufixo and linha.strip().endswith('-worker'):
        raise SystemExit(f"{comp} should NOT use the -worker image: {linha.strip()}")
PY

# The metrics port stays off the Service. On the http port it would sit behind
# the login; exposed through the Service it is one ServiceMonitor away from
# publishing every workflow and step name.
python3 - "$full" <<'PY' || erro "the metrics port leaked into the Service or the Ingress"
import re, sys
doc = open(sys.argv[1]).read()
# `kind: Service` matches `kind: ServiceAccount` as a substring, so anchor it.
def de(kind):
    return [d for d in doc.split('---') if re.search(r'^kind: %s$' % kind, d, re.M)]
svc = de('Service')
assert len(svc) == 1, 'expected exactly one Service, got %d' % len(svc)
assert '9090' not in svc[0], "the Service exposes 9090"
assert 'metrics' not in svc[0].split('ports:')[1], "the Service names a metrics port"
ing = de('Ingress')
assert len(ing) == 1, 'expected exactly one Ingress'
assert '9090' not in ing[0] and 'metrics' not in ing[0], "the Ingress routes metrics"
PY

# The API never talks to the API server, so it gets no token at all.
python3 - "$rendered" <<'PY' || erro "the API mounts a ServiceAccount token"
import sys
doc = open(sys.argv[1]).read()
b = [d for d in doc.split('---') if 'component: api' in d and 'kind: Deployment' in d][0]
assert 'automountServiceAccountToken: false' in b, "the API pod mounts a token"
PY

# The task identity has no Role: a step runs commands that came out of a YAML.
python3 - "$rendered" <<'PY' || erro "something binds a Role to the task ServiceAccount"
import sys
doc = open(sys.argv[1]).read()
task = [d for d in doc.split('---') if 'kind: ServiceAccount' in d and '-task' in d]
assert len(task) == 1, "expected exactly one task ServiceAccount"
for b in doc.split('---'):
    if 'kind: RoleBinding' in b or 'kind: ClusterRoleBinding' in b:
        assert '-task' not in b.split('subjects:')[-1], "a binding names the task account"
PY

# --- what has to be refused -------------------------------------------------
recusa() {
  local desc="$1"; shift
  if $HELM template t "$CHART" "$@" >/dev/null 2>&1; then
    erro "the chart accepted: $desc"
  fi
}
recusa "no database"            --set auth.user=a --set auth.passwordHash=h --set auth.secret=0123456789012345678901234567890123456789
recusa "two database sources"   "${MIN[@]}" --set database.existingSecret=s
recusa "no credential in prod"  --set database.url=x
recusa "a secret under 32 bytes" --set database.url=x --set auth.user=a --set auth.passwordHash=h --set auth.secret=short
recusa "an ingress with no host" "${MIN[@]}" --set api.ingress.enabled=true
recusa "tls with no secretName" "${MIN[@]}" --set api.ingress.enabled=true --set api.ingress.host=h --set api.ingress.tls.enabled=true
recusa "maxPods=0"              "${MIN[@]}" --set scheduler.maxPods=0
recusa "concurrency=0"          "${MIN[@]}" --set scheduler.concurrency=0
recusa "alerts with no webhook" "${MIN[@]}" --set alerts.enabled=true
recusa "more than one scheduler" "${MIN[@]}" --set scheduler.replicas=2
recusa "an unknown value"       "${MIN[@]}" --set nonsense=1

if [ "$falhas" -gt 0 ]; then
  echo "$falhas problem(s) with the chart."
  exit 1
fi
echo "✅ the chart renders, validates, and refuses what it should"
