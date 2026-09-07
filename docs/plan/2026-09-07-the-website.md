# The website: its source, its keys, and the Spanish it promises

**Written on** 2026-09-07 · **Base** the site as it stands at `master`
**Status** proposed — not started

The site landed one commit before the language rule was written, so it never
went through the English sweep. Its **content** is fine: that is exactly what
the rule exempts, and `site/content/pt/**` is the translation the rule exists to
protect.

Its **source** is not, and one of the three items here does not just violate a
rule — it makes `CONTRIBUTING.md` say something untrue.

---

## 1. Spanish is promised and missing — do this one first

```python
IDIOMAS = ["pt", "en"]        # site/build.py
```

```
Portuguese, English and Spanish.   # CONTRIBUTING.md
```

The site ships two. The contributing guide promises three, and it is the
document a new contributor reads first.

**Two honest resolutions, and leaving both as they are is not one of them.**

### Option A — ship the translation

The generator was built for it: a new entry in `IDIOMAS`, a block in
`i18n.json`, a directory under `content/`. Nothing in the templates or the CSS
changes.

The cost is the translation and only the translation:

| | |
|---|---|
| UI strings in `i18n.json` | **258 keys** |
| documentation pages | **12** |

That is a real piece of work, and it is work a machine translation should not
do alone for a page that teaches somebody to run a data pipeline.

### Option B — correct the sentence

One line in `CONTRIBUTING.md`, and the promise matches the product until the
translation is worth doing.

**Recommendation: B now, A when there is a Spanish-speaking reader to serve.**
Shipping a half-translated site is worse than shipping two languages honestly,
and the sentence is what is wrong today.

---

## 2. The `i18n.json` keys are Portuguese

258 keys, named `pular`, `nav_rotulo`, `hero_titulo`, `c1_valor`. They are
identifiers, so the rule covers them.

The awkward part is that they are referenced from `templates/*.html`, so
renaming is **two sides at once**. It is mechanical and it is not a
find-and-replace on one file.

### How to do it without breaking the build silently

The failure mode is a key renamed in `i18n.json` and missed in a template: the
generator emits an empty string, the page builds, and a heading is blank on a
page nobody opened.

So the order is:

1. **A check first.** Walk `templates/*.html` for every `{{ ... }}` reference,
   walk `i18n.json` for every key, and fail on either side having something the
   other does not. Run it before touching anything, to prove it passes today.
2. Rename, both sides in one commit per section.
3. The check stays in `build-site.yml`.

Step 1 is the whole safety, and it is worth having even if the rename never
happens: an unreferenced key and a missing key are both invisible today.

---

## 3. Source comments in Portuguese

| file | |
|---|---|
| `site/build.py` | the generator |
| `site/css/styles.css`, `site/css/docs.css` | |
| `site/js/main.js`, `site/js/docs.js` | |
| `site/README.md` | entirely Portuguese |

**95 comment lines across the code files**, plus the README.

This is the smallest and least urgent item in this document. It is debt, not a
defect: nothing behaves wrongly, and nobody is misled. It matters when a
contributor from outside opens `build.py`, which is the same argument the rest
of the sweep ran on.

Do it with the tool the sweep ended with: rename at code positions only, and fix
the comments by hand, reading each one. The site is 6 source files — small
enough that the hand-translation is an afternoon and the tooling is overkill.

---

## Order

| | why |
|---|---|
| **1. Spanish** | it is the only one that makes a document say something untrue, and option B is one line |
| **2. the i18n check** | worth having on its own; the rename can follow whenever |
| **2b. the key rename** | after the check exists, never before |
| **3. the comments** | debt, and the least of it |

## What this deliberately leaves alone

`site/content/pt/**` and `site/content/en/**` — the pages users read. Translating
those *away* is the opposite of the point.
