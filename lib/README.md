# Client libraries

One directory per language, each a separate artifact with its own version and
its own release tag.

| | | |
|---|---|---|
| [`python-context/`](python-context/) | `brevis` on PyPI | pass context between steps |

The directory says `python-context` and the package says `brevis`: the folder
names what it holds, and the distribution matches the import so that
`pip install brevis` gives you `from brevis import context`.

## What goes here, and what does not

These are **thin clients over a contract the engine defines**, not ports of the
Go SDK. The contract is two environment variables and a file, so each of them is
a JSON parse and a file write — small enough to read in one sitting, with no
dependencies, and testable with no cluster.

A port of the SDK's ETL machinery — drivers, pagination, checkpoints, ingestion
ids — does not belong here and probably nowhere: every language this would
target already has better tools for that, and Brevis's job is to run them.

## Why Go is not in this directory

Go's context support lives in the `sdk/` module it already publishes, as a
subpackage that prunes to nothing for anyone not importing it. Adding
`lib/go-context` would mean a second Go module for a package that has a module.

That is the one exception, and it is worth knowing before the second language
arrives: **the rule is one directory per language that does not already have a
published artifact.**
