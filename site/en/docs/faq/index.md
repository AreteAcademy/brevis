# FAQ

> The questions that come first, with short answers and where to go next.

*https://brevis.sh/en/docs/faq/ · brevis.sh docs (en)*

---

## Is brevis.sh really 100% open source?

Yes. The complete runtime is open to read, use, adapt and contribute to.

## What does the reference to Seneca mean for the project?

De Brevitate Vitae inspires more intentional engineering: reduce the complexity that consumes time, and preserve room for work with purpose.

More in [Philosophy](/en/docs/philosophy/index.md).

## How does pod isolation work?

Each workflow step runs as its own pod with its own image, keeping dependencies and execution context separate.

More in [Pod per step](/en/docs/pod-per-step/index.md).

## What is the relationship with Aretê Academy?

brevis.sh is a project of Aretê Academy, which brings great ideas from philosophy closer to the real challenges of building software.

## Can I use only one of the pieces?

Yes. Each piece has its own module, version and image: the SDK is a Go library that runs in any program, the Gateway starts on its own with a YAML file and no database, and brevis-sql runs as a container. The Core orchestrates the others when you want it to — it is a prerequisite for none of them.

More in [Ecosystem](/en/docs/ecosystem/index.md).
