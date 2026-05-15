# mhelm

**mhelm = Mirror HELM.** A supply-chain-secure mirror for a single Helm chart plus every container image it references. The CLI scaffolds and prepares; a GitHub Action runs the actual mirror, signs every artifact, attaches SLSA + MirrorProvenance to all of them, and SBOM + vuln scans to the images.

## Why

Mirroring a chart isn't just copying a `.tgz`. A real platform mirror needs:
- the chart bytes preserved by digest,
- every container image the chart references discovered and mirrored too,
- the override values that point a `helm install` at the mirror,
- upstream signatures verified before copying,
- downstream artifacts re-signed with your CI identity,
- SLSA build provenance + a custom MirrorProvenance attestation on every artifact, plus an SBOM + vuln scan per mirrored image,
- continuous drift detection against upstream rotation and downstream tampering.

mhelm does all of this with the lockfile (`chart-lock.json`) as the source of truth in git — every change is a reviewable PR.

## Architecture

Two trust zones:

| Zone | Where | What it does | Credentials |
|---|---|---|---|
| **CLI** (`mhelm`) | Dev laptop | Scaffold spec, discover images, verify upstream signatures, check drift. **Network-read-only.** | None |
| **Action** | GitHub Actions runner | Mirror chart + images, cosign-sign, attest, commit lockfile updates. | GHA ambient OIDC only |

Signing keys never touch a developer laptop. Every mirror is a reviewable diff of `chart-lock.json`.

## Quickstart — add your first chart

You prepare on your laptop (network-read-only, no credentials); CI mirrors and signs (ambient OIDC, no keys on your machine). One chart = one directory.

```
LAPTOP — read-only, no credentials          CI — writes + signs (OIDC)
─────────────────────────────────          ──────────────────────────────
1  mhelm init       scaffold                6  merge to main → mirror.yml:
2  edit chart.json + helm/values.yml           discover→verify→mirror→wrap
3  mhelm discover    → lock + values            →provenance→slsa→sign+attest
4  mhelm discover --check  (exit 0)             auto-commits lockfile back
5  commit + PR  ──dry-run gate──▶            7  drift.yml nightly → PR on drift
                                             8  release print-install | bash
```

1. **Scaffold** the chart directory with a starter `chart.json` + `helm/values.yml`:

   ```
   mhelm init platform/cilium \
     --upstream-type oci \
     --upstream-url oci://quay.io/cilium/charts/cilium \
     --upstream-version 1.19.3 \
     --downstream-url oci://ghcr.io/myorg/mirror
   ```

   For a classic HTTP Helm repo use `--upstream-type repo` with `--upstream-name <chart>` and an `https://` `--upstream-url`.

2. **Configure** `platform/cilium/chart.json`: add `mirror.discoveryValues`, any `mirror.extraImages` discovery can't auto-find, `mirror.verify` / `mirror.vulnPolicy`, and the optional `wrap` / `release` sections. Put deploy-shaping Helm values in `platform/cilium/helm/values.yml`. Field-by-field reference: [`chart.json` schema](#chartjson-schema). Full worked example: [`examples/cilium/`](examples/cilium/) ([`chart.json`](examples/cilium/chart.json)).

3. **Discover** — render the chart and pin every image (read-only):

   ```
   mhelm discover platform/cilium
   ```

   Writes `chart-lock.json` (the source of truth) and `image-values.yaml` (the `helm install --values` override). Both are committed.

4. **Sanity-check** — `mhelm discover --check platform/cilium` must exit `0`. This is the exact gate CI enforces; exit `2` means the lockfile is stale — re-run step 3. Optionally `mhelm verify platform/cilium` to inspect upstream signature posture before pushing.

5. **Commit + open a PR** — commit `chart.json`, `chart-lock.json`, `image-values.yaml`. Gate the PR with the Action in **`dry-run`** mode (`command: dry-run` → `discover --check` + `verify`, no writes/signing/commit): a stale lockfile fails the check, forcing fresh discover output before merge. This is the CI counterpart of step 4, wired by [`dry-run.yml`](examples/workflows/dry-run.yml).

6. **Land it → CI mirrors.** When the `chart.json` change lands on your default branch, [`mirror.yml`](examples/workflows/mirror.yml) (a `platform/**/chart.json` matrix) runs the full pipeline — discover → verify → mirror → wrap → provenance → slsa → cosign sign + attest (SBOM / vuln / SLSA / MirrorProvenance) — pushes chart + every image to your downstream registry, then opens a PR with the updated `chart-lock.json` / `image-values.yaml` / `mirror-provenance.json` (review the digest diff, merge to record the mirror in git). Details: [Canonical CI sequence](#canonical-ci-sequence), [GitHub Action](#github-action).

7. **Ongoing drift.** [`drift.yml`](examples/workflows/drift.yml) runs nightly and opens a PR per chart on upstream rotation, downstream tampering, or a new upstream version. Review it, bump `mirror.upstream.version`, and the loop returns to step 3.

8. **Deploy** — emit a runnable install for the locked artifact (the wrapper chart when `wrap` is set, otherwise the bare mirrored chart + `image-values.yaml`):

   ```
   mhelm release print-install platform/cilium | bash
   ```

**Wire CI:** copy [`dry-run.yml`](examples/workflows/dry-run.yml) (the step-5 PR gate), [`mirror.yml`](examples/workflows/mirror.yml), and [`drift.yml`](examples/workflows/drift.yml) from [`examples/workflows/`](examples/workflows/) into `.github/workflows/`, then adjust the chart matrix and `downstream.url`. Steps 1–5 are the only human loop; everything after the change lands is automated and auditable as git diffs.

## Files

| File | Producer | Committed | Role |
|---|---|---|---|
| `chart.json` | User | Yes | Input spec (`apiVersion: mhelm.io/v1alpha1`): `mirror` (upstream/downstream/discoveryValues/extraImages/verify/vulnPolicy), optional `wrap`, optional `release` |
| `chart-lock.json` | CLI + Action | Yes | Source of truth: chart digests, image list + per-image source/digest/values-paths/signature/downstream, drift records |
| `image-values.yaml` | `mhelm discover` | Yes | Sparse `helm install --values` override that points each matched values path at the mirror (skipped when a `wrap` section is configured) |
| `mirror-provenance.json` | `mhelm provenance` | (CI artifact) | Custom `mhelm.dev/MirrorProvenance/v1` predicate fed to `cosign attest` |
| `slsa-provenance.json` | `mhelm slsa` | (CI artifact) | SLSA v1 build provenance predicate fed to `cosign attest --type slsaprovenance1` |

Cargo.toml ↔ Cargo.lock pattern. One chart per `chart.json` / `chart-lock.json`. Multi-chart orchestration is the user's job (a `**/**/chart.json` matrix in CI).

### Recommended layout

Keep every Helm values file in a `helm/` subdirectory next to `chart.json`,
so `mirror.discoveryValues`, `wrap.valuesFiles`, and `release.valuesFiles`
all reference `helm/…` — one place to grep for "what overrides are we
applying", and uniform paths in `chart.json`:

```
platform/cilium/
├── chart.json
├── chart-lock.json
├── image-values.yaml          # generated by `mhelm discover`
└── helm/
    ├── values.yml             # discoveryValues + wrap/release valuesFiles
    └── install-overrides.yml  # optional opt-in overlay
```

`mhelm init` scaffolds `helm/values.yml` and pre-fills
`mirror.discoveryValues: ["helm/values.yml"]` so adopters fall into this
convention by default.

## `chart.json` schema

`apiVersion: mhelm.io/v1alpha1`. The flat v0.1.0 shape (no `apiVersion`,
top-level `upstream`/`downstream`/`valuesFiles`/`trustedIdentities`) is
still auto-migrated in memory with a stderr warning, but write new files
in the nested form:

```json
{
  "apiVersion": "mhelm.io/v1alpha1",
  "mirror": {
    "upstream":   { "type": "repo", "url": "https://charts.jetstack.io", "name": "cert-manager", "version": "v1.17.0" },
    "downstream": { "type": "oci",  "url": "oci://ghcr.io/myorg/mirror" },
    "discoveryValues": ["helm/values.yml"],
    "extraImages": [
      { "ref": "quay.io/cephcsi/cephcsi:v3.12.2", "valuesPath": "csi.cephcsi.image" }
    ],
    "verify": {
      "trustedIdentities": [
        { "subjectRegex": "https://github.com/cert-manager/.*", "issuer": "https://token.actions.githubusercontent.com" }
      ],
      "allowUnsigned": ["cilium/hubble-ui"]
    },
    "vulnPolicy": {
      "failOn": "critical",
      "allowlist": [ { "cve": "CVE-2024-1234", "expires": "2026-12-31", "reason": "tracked upstream; no patch yet" } ]
    }
  },
  "wrap":    { "version": "v1.17.0-myorg.1", "valuesFiles": ["helm/values.yml"], "extraManifests": [] },
  "release": { "name": "cert-manager", "namespace": "cert-manager", "valuesFiles": ["helm/install-overrides.yml"] }
}
```

| Field | Required | Description |
|---|---|---|
| `apiVersion` | yes | `mhelm.io/v1alpha1` (empty = legacy v0.1.0 auto-migrate). |
| `mirror.upstream.type` | yes | `repo` (classic HTTP Helm repo) or `oci`. |
| `mirror.upstream.url` | yes | Repo base URL, or the **full** `oci://registry/path/chart` ref. |
| `mirror.upstream.name` | `type=repo` only | Chart name. For `oci` it is rejected — put the full chart path in `url`. |
| `mirror.upstream.version` | yes | Semver (repo) or tag (oci). |
| `mirror.downstream.type` | yes | `oci` (only OCI destinations supported). |
| `mirror.downstream.url` | yes | Target registry path **without** the chart name. mhelm namespaces artifacts beneath it: `charts/<chart>` (faithful copy), `platform/<chart>` (wrapper), `images/<upstream-path>` (every image). |
| `mirror.discoveryValues` | no | YAML files merged in order during `discover` so rendered manifests match what you'll deploy. |
| `mirror.extraImages` | no | Manual list `[{ref, valuesPath?, overridePath?, reason?}]` for images discovery can't auto-find. `overridePath` emits the whole pinned ref as a single string (e.g. cilium's `image.override`). |
| `mirror.verify.trustedIdentities` | no | Allowlist for `mhelm verify`. When set, only matching cosign signatures are accepted. |
| `mirror.verify.allowUnsigned` | no | Repository paths exempt from verification (recorded `type=allowlisted`). |
| `mirror.vulnPolicy` | no | `failOn` (`critical`/`high`/`medium`/`never`) + `allowlist[{cve,expires,reason}]` for `mhelm vuln-gate`. |
| `wrap` | no | Author a wrapper chart depending on the mirror (`version`, `valuesFiles`, `extraManifests`). The wrapper reuses the mirrored chart's name under the `platform/` namespace; `version` is optional (defaults to the mirrored chart's version — set it to re-release independently). Image rewrites are auto-derived from the lockfile. |
| `release` | no | Deploy-time ergonomics for `mhelm release print-install` (`name`, `namespace`, `valuesFiles`). |

## Commands

```
mhelm init [dir]               scaffold chart.json + helm/values.yml stub
mhelm discover [dir]           pull chart, render, extract images, resolve digests, write chart-lock.json + image-values.yaml
mhelm discover --check [dir]   compute artifacts but don't write; exit 2 if they would change (PR gate)
mhelm verify [dir]             cosign-verify every upstream image; record signature data in chart-lock.json (--strict to fail)
mhelm mirror [dir]             push chart + every image to downstream OCI; record downstream refs/digests
mhelm wrap [dir]               author + push a wrapper chart depending on the mirror (no-op without a wrap section)
mhelm release init [dir]       scaffold the chart.json release section
mhelm release print-install [dir]  emit a runnable `helm upgrade --install` against the locked artifact
mhelm provenance [dir]         write mirror-provenance.json (custom MirrorProvenance predicate)
mhelm slsa [dir]               write slsa-provenance.json (SLSA v1 build provenance predicate)
mhelm vuln-gate [dir]          apply chart.json#mirror.vulnPolicy to a cosign vuln/v1 report
mhelm refs [dir]               print downstream ref@digest lines (--with-upstream pairs, --json, --chart-only, --images-only)
mhelm drift [dir]              detect upstream rotation, downstream tampering, new upstream versions
mhelm version                  print the mhelm version (git-tag derived)
```

All commands take an optional `[dir]` positional (default `.`) — the directory holding `chart.json`. `init` creates the directory if it doesn't exist.

### Canonical CI sequence

```
mhelm discover   → images, digests, values paths, source labels
mhelm verify     → upstream signatures
mhelm mirror     → push chart + images; downstream digests
mhelm wrap       → author + push wrapper chart (when a wrap section is set)
mhelm provenance → MirrorProvenance predicate
mhelm slsa       → SLSA v1 predicate
mhelm refs       → ref@digest lines feeding cosign sign + attest
cosign sign + attest (SBOM via syft, vuln via grype, SLSA, MirrorProvenance)  [GHA only]
mhelm drift (scheduled) → drift records committed via PR
```

`discover`, `verify`, `provenance`, `slsa`, `refs`, `drift` are all network-read-only. `mirror` performs registry writes. `cosign sign + attest` run only in CI with ambient OIDC.

## Image discovery sources

Each `chart-lock.json` image entry carries a `source` label so reviewers can audit where it came from:

| Source | Pattern |
|---|---|
| `manifest` | `containers[].image` / `initContainers[].image` from rendered K8s manifests |
| `annotation` | `Chart.yaml` `artifacthub.io/images` |
| `env` | `containers[].env[].value` (regex-matched, registry-validated) |
| `configmap` | Any ConfigMap `data` value (regex-matched, registry-validated) |
| `crd-spec` | Any string in a non-builtin Kind (regex-matched, registry-validated) |
| `manual` | `chart.json#extraImages` |

Regex-discovered candidates only enter the lockfile if `crane.Digest` confirms they're pullable. Random strings that happen to look like refs are filtered out. Trusted sources (`manifest`, `annotation`, `manual`) are kept regardless of registry resolution; untrusted (`env`, `configmap`, `crd-spec`) are dropped on resolution failure.

Smoke-verified against cert-manager, cilium, rook-ceph (8 images — 1 manifest + 7 ConfigMap), rook-ceph-cluster (1 image — CRD spec), argocd, cnpg.

## GitHub Action

`action.yml` at repo root ships a composite Action that runs the full pipeline in a single step.

Minimal consumer workflow:

```yaml
permissions:
  contents: write   # commit lockfile updates back
  packages: write   # push to ghcr.io
  id-token: write   # cosign keyless OIDC

jobs:
  mirror:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: docker/login-action@v3
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}
      - uses: gilsstudio/mhelm@v0.6.0
        with:
          dir: platform/cert-manager
```

### Inputs

| Input | Default | Description |
|---|---|---|
| `dir` | (required) | Directory containing `chart.json`, relative to checkout root. |
| `command` | `mirror` | `mirror` (full pipeline), `dry-run` (`discover --check` + `verify` only — no mirror/sign/commit; fails on a stale lockfile), or `drift` (read-only divergence check). |
| `cosign-version` | `v2.6.3` | cosign release to install. |
| `sign` | `true` | Mirror mode: sign + attest every downstream artifact. |
| `verify` | `true` | Mirror/dry-run mode: run `mhelm verify` between discover and mirror. |
| `strict-verify` | `false` | Fail when an upstream image is unverified — including `unreachable` (trust roots / registry blocked). Air-gapped runners must provide offline trust roots or set this `false`. |
| `copy-upstream-attestations` | `true` | Mirror mode: forward upstream cosign signatures + attestations into the downstream registry. Best-effort. |
| `drift-exit-zero` | `true` | Drift mode: exit 0 on findings (PR-friendly). Set `false` to fail the job. |

Outputs: `lockfile`, `mirror-values` (path to the generated `image-values.yaml`; the output id is kept for backward compatibility), `provenance` — absolute paths to the generated files. Empty in `dry-run` mode (no artifacts written).

### Per-artifact attestation chain

When `sign=true`, **every** downstream artifact (the mirrored chart, the optional wrapper chart, and every image) gets:

1. **cosign signature** — keyless via Fulcio + Rekor (ambient GHA OIDC).
2. **Forwarded upstream attestations** — `cosign copy --force` from upstream registry, best-effort.
3. **SLSA v1 build provenance** — from `slsa-provenance.json` → `cosign attest --type slsaprovenance1`.
4. **MirrorProvenance** — from `mirror-provenance.json` → `cosign attest --type https://mhelm.dev/MirrorProvenance/v1`.

**Images additionally** get:

5. **CycloneDX SBOM** — `syft <ref>` → `cosign attest --type cyclonedx`.
6. **Vulnerability report** — `grype -o template -t cosign-vuln.tmpl` → `cosign attest --type vuln` (cosign vuln/v1 schema), gated by `chart.json#mirror.vulnPolicy` before attesting.

The chart and wrapper are Helm OCI artifacts (`application/vnd.cncf.helm.*` media types) that syft/grype cannot catalog, so they receive signature + SLSA + MirrorProvenance only — there is no package SBOM for a chart's templated YAML.

All attestations are stored as OCI referrers keyed on the artifact's manifest digest and logged to Rekor.

### Example workflows

- [`examples/workflows/mirror.yml`](examples/workflows/mirror.yml) — multi-chart matrix mirror with auto-commit.
- [`examples/workflows/drift.yml`](examples/workflows/drift.yml) — nightly drift check, opens a PR per chart with findings.

## SLSA position

**The mirror operation can reach SLSA L3** when run via the Action:
- Provenance generated by the build platform (GHA), not user code.
- Non-forgeable signer key (Fulcio short-lived cert via OIDC).
- Isolated ephemeral builder (GHA runner).
- Provenance signed and logged to Rekor.

**The upstream artifact's build provenance** is whatever the publisher published. mhelm preserves it (`cosign copy` forwards referrers) but cannot elevate it. End-to-end statement consumers can verify:

> *"this image was built by publisher P (SLSA-Ln from P's attestation), mirrored by mhelm in GHA (SLSA-L3 from mhelm's attestation), and is byte-identical to what P published."*

For upstreams that don't publish attestations, mhelm's MirrorProvenance is the only attestation — the chain only goes back to "mhelm copied X from URL Y byte-for-byte at run Z."

## Trust model summary

1. Upstream publisher signs → `mhelm verify` checks before mirror.
2. mhelm (GHA OIDC identity) signs every downstream artifact.
3. Downstream consumer verifies mhelm's signatures at deploy time (admission policy — outside mhelm scope).
4. `chart-lock.json` in git = reproducible, reviewable, auditable.
5. Rekor = transparency log of every mirror operation.
6. `mhelm drift` = continuous detection of upstream rotation and registry tampering.

Each link is independently verifiable. Compromise of any single link is detectable.

## Platform bootstrap (cluster-side, informational)

"Only deploy from my private registry" needs three links — mhelm covers the first two:

1. **Mirror + override** (mhelm) — find every image, mirror it, produce `image-values.yaml`.
2. **`helm install --values image-values.yaml`** (user) — applies the override.
3. **Admission-time enforcement** (cluster-side, **outside mhelm scope**) — reject any Pod whose image doesn't start with your mirror prefix.

Link 3 has a chicken-and-egg: Kyverno and OPA Gatekeeper both need a working CNI to receive admission webhook calls, but CNI is one of the charts you want to enforce policy on. Three options:

- **Containerd registry mirrors at the node level** (recommended). Bake the redirect into node images; every pull (including CNI's) goes through the mirror at the runtime layer.
- **ValidatingAdmissionPolicy** (K8s 1.30+). Native CEL policies in the apiserver — no Pods, no CNI dependency.
- **Pre-load CNI images** onto nodes via `ctr -n=k8s.io images import` at node-build time.

Writing the enforcement layer is a one-time step against `chart.json#downstream.url` — intentionally outside mhelm scope.

## Library stack

| Concern | Library / Tool | Why |
|---|---|---|
| Chart pull (HTTP repo) | `helm.sh/helm/v3/pkg/{repo,getter}` | Standard Helm SDK |
| Chart pull/push (OCI) | `helm.sh/helm/v3/pkg/registry` | Wraps oras-go with Helm media types |
| Template rendering | `helm.sh/helm/v3/pkg/engine` | Catches templated image refs |
| Image streaming mirror | `github.com/google/go-containerregistry/pkg/crane` | Blob-level copy, no full pull/push |
| Image ref parsing | `github.com/google/go-containerregistry/pkg/name` | Canonical repo normalization |
| Upstream signature verify (CLI) | `github.com/sigstore/cosign/v2/pkg/...` | Embedded so `mhelm verify` works without forcing a runtime cosign install |
| Downstream sign + attest (Action) | `sigstore/cosign-installer` CLI | Cosign CLI shelled out from CI — official, pinnable, no Go-API churn |
| SBOM (Action) | `anchore/sbom-action` (syft CLI) | CycloneDX/SPDX, official Action |
| Vuln scan (Action) | `anchore/scan-action` (grype CLI) | Pairs natively with syft |
| Custom predicates | plain `encoding/json` | Cosign wraps the predicate body in its own DSSE envelope |

**Tool boundary.** The CLI does scaffolding, discovery, verification, mirroring, and predicate generation. The Action composes the CLI with cosign / syft / grype installers. mhelm deliberately does **not** embed cosign/syft/grype as Go libraries for signing — they're battle-tested CLIs, version-pinnable in the workflow, and immune to Go-API churn.

## Auth

`mhelm mirror` reuses `~/.config/helm/registry/config.json` and `~/.docker/config.json` (the latter is what crane uses for image copies). Run `helm registry login` and/or `docker login` before mirroring to private registries.

## Local-registry testing

Set `MHELM_INSECURE=1` to allow plain HTTP + skip TLS verify. Required when targeting a local OCI registry like `registry:2` on `localhost`. Leave unset for production registries.

## Known limitations

- **Hardcoded operator binary defaults** — image refs baked into the operator's Go binary itself (no env, no ConfigMap). Add to `chart.json#extraImages` once a production install reveals them.
- **Sub-chart dependencies** require `helm dependency update` first; user runs this before `mhelm discover`.
- **Multi-chart bundles** — one chart per repo/dir. Use multiple Action runs or a `**/**/chart.json` matrix in CI.
- **Vuln remediation** — mhelm scans and attests; it does not patch or block on findings. Deploy-time policy (Kyverno, sigstore-policy-controller) does the blocking.
- **Values-path matching is heuristic** — accuracy `heuristic` (exact canonical-repo match), `suffix-heuristic` (a hyphen-suffix extension like cilium's `operator` → `operator-generic`), or `manual` (`extraImages.valuesPath`). A future dual-render-diff mode would upgrade unambiguous cases to `verified`. For images that still don't match, set `extraImages.valuesPath` (and `overridePath` for charts with an `image.override` escape hatch), or fill `image-values.yaml` by hand.
- **`mhelm verify` is best-effort against egress** — an image whose signature can't be checked because Fulcio/Rekor or the registry is unreachable is recorded as `type=unreachable` (distinct from `none` = genuinely unsigned). Both fail `--strict`.
