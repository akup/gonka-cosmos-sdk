# Publishing a version tag so consumers can use the fork

To let other modules (e.g. inference-chain) refer to this SDK fork by version in `go.mod`:

```text
replace github.com/cosmos/cosmos-sdk => github.com/gonka-ai/cosmos-sdk v0.53.3-ps15
```

you must **publish a Git tag** on the repository that `go.mod` points to (`github.com/gonka-ai/cosmos-sdk`). Go resolves module versions from the VCS (Git); there is no separate package registry.

## Steps

### 1. Put the code you want to release on a branch and push it

```bash
# From the repo root (gonka-cosmos-sdk)
git checkout dev/v0.53.3-ps15   # or the branch you want to tag
git add -A && git commit -m "Your release changes"   # if you have uncommitted changes
git push origin dev/v0.53.3-ps15
```

If the consumer’s `go.mod` uses **github.com/gonka-ai/cosmos-sdk**, that branch (and the tag below) must be pushed to **gonka-ai/cosmos-sdk**. If your `origin` is a different fork (e.g. `akup/gonka-cosmos-sdk`), add that repo as a remote and push there, or push to the gonka-ai repo (e.g. `git push upstream dev/v0.53.3-ps15`).

### 2. Create an annotated tag

Use a **semver-like tag** that matches what you put in `go.mod` (e.g. `v0.53.3-ps15`):

```bash
git tag -a v0.53.3-ps15 -m "Release v0.53.3-ps15"
```

### 3. Push the tag to the same repo that go.mod points to

```bash
# If the consumer uses github.com/gonka-ai/cosmos-sdk:
git push upstream v0.53.3-ps15

# Or if your origin is already gonka-ai/cosmos-sdk:
git push origin v0.53.3-ps15
```

After this, `go mod download` and `go build` in inference-chain (or any module that has the replace to `github.com/gonka-ai/cosmos-sdk v0.53.3-ps15`) will resolve the module from that tag.

## inference-chain go.mod

Your inference-chain `go.mod` already has:

```go
replace (
	github.com/cosmos/cosmos-sdk => github.com/gonka-ai/cosmos-sdk v0.53.3-ps15
	// ...
)
require (
	github.com/cosmos/cosmos-sdk v0.50.14  // version here is ignored for the replace
	// ...
)
```

So you only need to ensure the tag **v0.53.3-ps15** exists on **github.com/gonka-ai/cosmos-sdk**. No extra “publish” step; the Git tag is the published version.

## Summary

| Goal | Action |
|------|--------|
| Publish a version | Create annotated tag (e.g. `v0.53.3-ps15`) and push it to `github.com/gonka-ai/cosmos-sdk`. |
| Consume it | In `go.mod`: `replace github.com/cosmos/cosmos-sdk => github.com/gonka-ai/cosmos-sdk v0.53.3-ps15` (already set in inference-chain). |
