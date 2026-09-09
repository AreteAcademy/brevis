# AI as a transform, and the two invariants it breaks

**Written on** 2026-09-09 · **Base** `sdk/v0.58.0`
**Status** analysis — `NOTES.md` §13, `TASK.md` #13

> *"precisamos ter no nosso fluxo alguma forma de chamar AI como transform dos
> dados que coletamos via extract ... cada usuario poder invocar Gemini, OpenAI,
> Anthropic ou OpenRouter de forma facil e simples."*

---

## 1. The seam already exists

```go
type Transformer func(payload any) (any, error)
```

An AI call fits that signature today, with no SDK change at all: a fetcher can
already `POST` to OpenAI inside a `Transform`. So the question is not *can it* —
it is **what the SDK should own so that everybody does not write the same four
hundred lines badly.**

## 2. What makes AI different from every other transform

`transform.go` says what the seam is for: *"the shaping a row needs before it is
worth storing at all — dropping request metadata, renaming a field, deriving a
value the source gives in pieces."* Every one of those is **free, instant and
deterministic**.

An AI call is none of the three, and two of those break invariants this SDK
already relies on:

| | every other transform | an AI call |
|---|---|---|
| cost | zero | **money, per record** |
| latency | microseconds | hundreds of milliseconds |
| determinism | the same input gives the same output | **it does not** |
| failure | a bug | a rate limit, a timeout, a refusal |

**The determinism one is the serious one.** The SDK's whole retry story rests on
a rerun producing the same bytes — that is what `ingestion_id` means, what makes
a checkpoint resumable, and why a retried run is safe. An AI transform makes a
second attempt produce *different data for the same input*, silently.

**The cost one is the expensive one.** A retry of a 50,000-record load re-calls
the model 50,000 times. Today a retry is free; here it has a bill.

Neither is a reason not to build it. Both are reasons the design has to name
them instead of discovering them in an invoice.

## 3. What the SDK should own

**One client, not four.** OpenAI, Anthropic, Gemini and OpenRouter are four
vendors and — for chat completion — one wire format: OpenRouter *is* an
OpenAI-compatible endpoint fronting all of them, and OpenAI-compatible endpoints
exist for the others. Writing four clients buys nothing except four things to
keep working.

```go
sdk.AI{
    BaseURL: "https://openrouter.ai/api/v1",   // or api.openai.com, or …
    Model:   "anthropic/claude-sonnet-4",
    Prompt:  "Classify this incident report. Answer with one of: …",
    Into:    "category",
}
```

The vendor becomes a URL and a model string, which is what it is. Whoever needs
a shape only one vendor has writes the HTTP call themselves — the same boundary
`CreateSQL` draws for a warehouse.

**What it must do that a hand-written call would get wrong:**

1. **Batch.** One request per record is the naive shape and it is also the one
   that costs the most and takes longest. N records per call, with the ceiling
   as a field.
2. **Cache by input hash.** The same input in the same run must not be paid for
   twice, and a retry of a run should reuse what the first attempt already
   bought. The checkpoint machinery in `checkpoint.go` is the precedent and
   possibly the mechanism.
3. **Refuse silently-wrong output.** A model asked for one of five categories
   will occasionally answer a sixth, or a sentence. A declared set of allowed
   answers, and a record that fails it is `SkipRecord` or an error — never a
   value that lands in a warehouse looking like data.
4. **Rate limits are a 429, and 429 is a retry**, not a failure. The SDK
   already knows how to do this for `from.HTTP`.
5. **The key is a credential**, and this SDK already has a credential store and
   a rule about where secrets do not go.

## 4. What it must NOT do

**Not a chain, not an agent, not tools.** The moment this grows a second call
that depends on the first, it is a framework, and the SDK's stated position is
that heavy reshaping belongs downstream in dbt. One call, one field, one answer.

**Not embeddings, at first.** A different shape (a vector column, a distance
function, a destination that supports it) and a different plan.

**Not a default model.** A default here is a bill somebody did not choose and a
vendor somebody did not pick.

## 5. The honest risk, and it is not technical

An AI transform that is easy makes it easy to send **every record of a
production table to a third party**. The SDK is used on government data,
financial data and personal data. Whatever this looks like, it should be
impossible to enable by accident, and the documentation should say — in the
first paragraph, not a footnote — that records leave the building.

## 6. Where it belongs

**Its own module**, `sdk/ai`, for the same reason `otelmeter` and DuckDB are:
nobody who does not want it should link an HTTP client with retry, a token
counter and a JSON-schema validator. And the pruning gate proves it.

## 7. Order

This is a market position, not a feature: the ask says *"sem ele teremos um
atraso em relação ao mercado"*, and that is a reasonable read. But it is also
the item on the list with the most ways to be wrong quietly — a cache that
misses, a validator that passes a hallucination, a retry that doubles a bill.

It goes after the cheap dlt asks and before the third executor. It should ship
with a worked example against a real provider, and the first thing that example
should print is the cost.
