# The mark

```
     ⌣          brevis.sh
```

The mark is the **breve**: in Latin scansion, the sign that marks a *short*
syllable. Its counterpart, the macron, marks the long one.

```
   brĕvis        the e is short — this is the correct scansion
   brēvis        the macron would say long — wrong for this word
```

So the mark is the word itself, written as the sign that names it. `brevis` is
Latin for *short*; the breve is what "short" looks like when a Roman grammarian
writes it down. And the source of the name is a Latin text — Seneca's *De
Brevitate Vitae*, on how little of our time we actually keep.

## What it replaced, and why

The previous mark was three nodes converging: two sources feeding a
destination. It was a good drawing of what an orchestrator does — and that is
the problem. **Any** orchestrator could wear it. Airflow, Dagster, Prefect and
Kestra all move a graph from left to right, and a logo that describes the
category identifies nobody inside it.

It was also inherited. It came from Aretê Academy's era of the project, when the
console still carried parchment and Cormorant, and it stayed after the product
had a name of its own.

The breve says something only this project can say.

## Drawing it

```
M4 10.4C4 15.2 20 15.2 20 10.4    stroke-width 3, round caps, 24×24 box
```

**Wide and shallow: 16 units across, 3.6 deep.** That ratio is the whole
difference between a breve and a smile, and it was found by drawing the first
version wrong — 11.6 by 4.0, which at 128px read as a grin. The proportions
were then calibrated against IBM Plex Mono's own `ĕ`, since that is the typeface
the mark sits beside.

**Round caps, not butt.** The interface's square corners are containers, not
stroke terminals; at 16px — a browser tab, the sidebar — butt ends look broken.
Sixteen pixels is the size that decides a mark, not 512.

**`currentColor` throughout.** One file serves the dark site, the light console
and any client's palette, with no variant per theme. The only exception is the
favicon, which carries a solid `#141711` ground because a tab is light on some
systems and dark on others, and a bare mark disappears against one of them.

| file | used by |
|---|---|
| [`site/assets/mark.svg`](../site/assets/mark.svg) | the website |
| [`site/assets/favicon.svg`](../site/assets/favicon.svg) | browser tabs |
| [`web/assets/logo.svg`](../web/assets/logo.svg) | the console, and any client brand |

The three carry the same path. If one changes, all three change.

## The wordmark

`brevis.sh`, set in **IBM Plex Mono Medium**, with `.sh` in the olive. Mono
because the name is a shell command, and the TLD is part of it.

The mark sits to the left, optically centred on the x-height, at roughly 1.05×
the type size. Do not stack them, do not box them, and do not set the wordmark
in the serif — there is no serif in this identity any more.

Written in prose it is always **brevis.sh**, lowercase, never *Brevis.sh* or
*BREVIS*. The scanned form `brĕvis` belongs to the identity's own explanation —
this page, the site's About — and not to running text, where it would break
copy-paste and search.

## Colour

The palette is the site's, and the console inverts the ground: `#141711` is the
site's *background* and the console's *text*. Same hues, opposite footing —
a landing page is read for two minutes in any light; a console is read for hours
in a bright office.

| token | value | on `#141711` |
|---|---|---|
| text | `#ecefe4` | 15.6:1 |
| dim text | `#a3ad97` | 7.7:1 |
| lime — accent, active state | `#c7d66d` | 11.4:1 |
| olive — labels, detail | `#7d8e50` | 5.1:1 |
| green-2 — structural rule | `#38412b` | 1.7:1 |

**Every ratio here is measured, not asserted.** `--green-2` at 1.7:1 is a
hairline and nothing else: never text, never the outline of a control. That is
what the olive is for. A palette whose contrast is claimed rather than computed
fails on some screen, and nobody finds out which.

## Type

| | |
|---|---|
| IBM Plex Sans | prose |
| IBM Plex Mono | labels, commands, data, the wordmark |

The console serves both **from the binary** — a UI that depends on Google Fonts
changes typeface halfway down the screen inside a cluster with no route out.
92 KB for the pair, against 208 KB for the Inter and Cormorant it replaced.

Cormorant Garamond is gone from both surfaces. It was Aretê Academy's voice, and
a brochure's vocabulary on a screen whose job is a table of run ids.

## Using it

**Do**

- give the mark clear space of at least its own cap height on every side;
- let it inherit colour from its surroundings;
- use the favicon file, not the plain mark, anywhere a solid ground is needed.

**Do not**

- rotate, mirror or flip it — a flipped breve is a macron, which means the
  opposite;
- fill the arc, add a circle behind it, or give it a gradient;
- redraw it deeper "so it reads better small". It was already tried; it becomes
  a smile.
