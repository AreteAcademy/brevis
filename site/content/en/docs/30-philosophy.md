---
title: Philosophy
description: Where brevis.sh comes from: Seneca, Aretê Academy, and what that decides in the code.
group: The project
order: 30
slug: philosophy
serif: true
---

brevis.sh is born inside Aretê Academy: discipline, purpose and excellence applied to building software.

## De brevitate vitae

> It is not that we have little time, but that we lose much of it.

Seneca's maxim guides the question behind brevis.sh: which complexities truly deserve a team's time? Every workflow runs in isolated pods so that failures, versions and dependencies have clear boundaries.

Four choices in the runtime come out of that question:

- **image per step** — Each step declares the runtime it needs. Bumping one version doesn't force you to touch the others.
- **logs per run** — Output belongs to the run that produced it. Investigating a failure isn't rebuilding a lost context.
- **persistent retries** — The attempt outlives the process. A restart doesn't erase what was already known about the work.
- **dependency isolation** — What one step carries doesn't leak into its neighbours. A clear boundary is what makes a failure legible.

## What the project proposes

Between *philosophy without practice* and *technology without direction*, **clarity to do what matters.**

## Tools shape the way we think.

> “Sustainable code is not merely code that keeps running. It is code that leaves room for people to reason.” — Philosophy applied to software

> “Clarity is not the opposite of sophistication. It is the criterion that keeps sophistication from becoming noise.” — Open source by principle
