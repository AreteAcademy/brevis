# Publishing the image

Two images come out of the same `Dockerfile`, from the same binary:

| tag | base | role |
|---|---|---|
| `daniel3843/brevis:<version>` | distroless | API and UI. It runs nothing, so it has no shell. |
| `daniel3843/brevis:<version>-worker` | alpine | `scheduler`, `publish`, `backfill`. **It has a shell**, because the workflows' `run:` steps need one. |

The split is not fussiness: the worker runs arbitrary commands out of the
client's YAML, and the API does not. Giving the API a shell would widen the
surface of the network-exposed process by the component that needs it least.

## By hand

```bash
docker login -u daniel3843          # a Docker Hub token, not the password
make image                          # local architecture, for testing
make image-smoke                    # checks the version and the shell
make image-push                     # multi-arch (amd64 + arm64), publishes
```

`make image-push` creates a `buildx` builder the first time. Publishing into
another namespace requires editing no file:

```bash
make image-push NAMESPACE=another-account
make image-push REGISTRY=us-central1-docker.pkg.dev NAMESPACE=project/repo
```

## Through CI

`.github/workflows/release.yml` publishes on every `v*` tag. It needs two
secrets on the repository: `DOCKERHUB_USERNAME` and `DOCKERHUB_TOKEN` (Docker
Hub → Account Settings → Personal access tokens, *Read & Write* permission).

```bash
echo 0.2.0 > VERSION
git commit -am "release: 0.2.0"
git tag v0.2.0 && git push origin master --tags
```

CI refuses the tag when `VERSION` does not match it -- otherwise `brevis
version` inside the image would lie about the tag that published it, and that
is what an incident is traced by.

## Why multi-arch

`linux/amd64` and `linux/arm64`. Development is on Apple Silicon; an amd64-only
image runs on a Mac through emulation -- slowly, and hiding architecture
problems until the deploy. The `Dockerfile` cross-compiles
(`--platform=$BUILDPLATFORM` on the build stage), so nothing runs under QEMU:
it is seconds, not minutes.

## Checking what was published

```bash
docker run --rm daniel3843/brevis:0.1.0 version
docker buildx imagetools inspect daniel3843/brevis:0.1.0   # checks both architectures
```
