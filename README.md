# mhelm

**mhelm = Mirror HELM.** A supply-chain-secure mirror for a single Helm chart plus every container image it references. The CLI scaffolds and prepares; a GitHub Action runs the actual mirror, signs every artifact, and attaches SBOM / vuln / SLSA / MirrorProvenance attestations.

## Why

Mirroring a chart isn't just copying a `.tgz`. A real platform mirror needs:
- the chart bytes preserved by digest,
- every container image the chart references discovered and mirrored too,
- the override values that point a `helm install` at the mirror,
- upstream signatures verified before copying,
- downstream artifacts re-signed with your CI identity,
- per-artifact SBOM + vuln scan + SLSA build provenance + a custom attestation tying the whole operation together,
- continuous drift detection against upstream rotation and downstream tampering.

mhelm does all of this with the lockfile (`chart-lock.json`) as the source of truth in git — every change is a reviewable PR.

## Architecture

Two trust zones:

| Zone | Where | What it does | Credentials |
|---|---|---|---|
| **CLI** (`mhelm`) | Dev laptop | Scaffold spec, discover images, verify upstream signatures, check drift. **Network-read-only.** | None |
| **Action** | GitHub Actions runner | Mirror chart + images, cosign-sign, attest, commit lockfile updates. | GHA ambient OIDC only |

Signing keys never touch a developer laptop. Every mirror is a reviewable diff of `chart-lock.json`.

## Files

| File | Producer | Committed | Role |
|---|---|---|---|
| `chart.json` | User | Yes | Input spec: upstream ref, downstream registry, optional `valuesFiles`, optional `extraImages` |
| `chart-lock.json` | CLI + Action | Yes | Source of truth: chart digests, image list + per-image source/digest/values-paths/signature/downstream, drift records |
| `mirror-values.yaml` | `mhelm discover` | Yes | Sparse `helm install --values` override that points each matched values path at the mirror |
| `mirror-provenance.json` | `mhelm provenance` | (CI artifact) | Custom `mhelm.dev/MirrorProvenance/v1` predicate fed to `cosign attest` |
| `slsa-provenance.json` | `mhelm slsa` | (CI artifact) | SLSA v1 build provenance predicate fed to `cosign attest --type slsaprovenance1` |

Cargo.toml ↔ Cargo.lock pattern. One chart per `chart.json` / `chart-lock.json`. Multi-chart orchestration is the user's job (a `**/**/chart.json` matrix in CI).

## `chart.json` schema

```json
{
  "upstream": {
    "type": "repo",
    "url": "https://charts.jetstack.io",
    "name": "cert-manager",
    "version": "v1.17.0"
  },
  "downstream": {
    "type": "oci",
    "url": "oci://ghcr.io/myorg/mirror"
  },
  "valuesFiles": ["values-override.yaml"],
  "extraImages": [
    { "ref": "quay.io/cephcsi/cephcsi:v3.12.2", "valuesPath": "csi.cephcsi.image" }
  ],
  "trustedIdentities": [
    { "subjectRegex": "https://github.com/cert-manager/.*", "issuer": "https://token.actions.githubusercontent.com" }
  ]
}
```

| Field | Required | Description |
|---|---|---|
| `upstream.type` | yes | `repo` (classic HTTP Helm repo) or `oci`. |
| `upstream.url` | yes | Repo base URL, or full `oci://registry/path/chart` ref. |
| `upstream.name` | when `type=repo` | Chart name. Derived from URL for `oci`. |
| `upstream.version` | yes | Semver (repo) or tag (oci). |
| `downstream.type` | yes | `oci` (only OCI destinations supported). |
| `downstream.url` | yes | Target registry path **without** the chart name (e.g. `oci://ghcr.io/myorg/mirror`). |
| `valuesFiles` | no | YAML overrides merged in order during discover so rendered manifests match what you'll deploy. |
| `extraImages` | no | Manual list `[{ref, valuesPath?}]` for images discovery can't auto-find (hardcoded operator defaults, etc.). |
| `trustedIdentities` | no | Allowlist for `mhelm verify`. When set, only matching cosign signatures are accepted. |

## Commands

```
mhelm init [dir]         scaffold chart.json
mhelm discover [dir]     pull chart, render, extract images, resolve digests, write chart-lock.json + mirror-values.yaml
mhelm verify [dir]       cosign-verify every upstream image; record signature data in chart-lock.json
mhelm mirror [dir]       push chart + every image to downstream OCI; record downstream refs/digests
mhelm provenance [dir]   write mirror-provenance.json (custom MirrorProvenance predicate)
mhelm slsa [dir]         write slsa-provenance.json (SLSA v1 build provenance predicate)
mhelm refs [dir]         print downstream ref@digest lines (--with-upstream for cosign-copy pairs)
mhelm drift [dir]        detect upstream rotation, downstream tampering, new upstream versions
```

All commands take an optional `[dir]` positional (default `.`) — the directory holding `chart.json`. `init` creates the directory if it doesn't exist.

### Canonical CI sequence

```
mhelm discover   → images, digests, values paths, source labels
mhelm verify     → upstream signatures
mhelm mirror     → push chart + images; downstream digests
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
      - uses: gilsstudio/mhelm@v1
        with:
          dir: platform/cert-manager
```

### Inputs

| Input | Default | Description |
|---|---|---|
| `dir` | (required) | Directory containing `chart.json`, relative to checkout root. |
| `command` | `mirror` | `mirror` (full pipeline) or `drift` (read-only divergence check). |
| `cosign-version` | `v2.6.3` | cosign release to install. |
| `sign` | `true` | Mirror mode: sign + attest every downstream artifact. |
| `verify` | `true` | Mirror mode: run `mhelm verify` between discover and mirror. |
| `strict-verify` | `false` | Mirror mode: fail when an upstream image is unverified. |
| `copy-upstream-attestations` | `true` | Mirror mode: forward upstream cosign signatures + attestations into the downstream registry. Best-effort. |
| `drift-exit-zero` | `true` | Drift mode: exit 0 on findings (PR-friendly). Set `false` to fail the job. |

Outputs: `lockfile`, `mirror-values`, `provenance` — absolute paths to the generated files.

### Per-artifact attestation chain

When `sign=true`, every downstream artifact (chart + each image) gets:

1. **cosign signature** — keyless via Fulcio + Rekor (ambient GHA OIDC).
2. **Forwarded upstream attestations** — `cosign copy --force` from upstream registry, best-effort.
3. **CycloneDX SBOM** — `syft <ref>` → `cosign attest --type cyclonedx`.
4. **Vulnerability report** — `grype -o cosign-vuln` → `cosign attest --type vuln` (cosign vuln/v1 schema).
5. **SLSA v1 build provenance** — from `slsa-provenance.json` → `cosign attest --type slsaprovenance1`.
6. **MirrorProvenance** — from `mirror-provenance.json` → `cosign attest --type https://mhelm.dev/MirrorProvenance/v1`.

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

1. **Mirror + override** (mhelm) — find every image, mirror it, produce `mirror-values.yaml`.
2. **`helm install --values mirror-values.yaml`** (user) — applies the override.
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
- **Values-path matching is heuristic** — accuracy `heuristic` in the lockfile. A future dual-render-diff mode would upgrade to `verified` for unambiguous cases. Until then, unmatched images carry no `valuesPaths` and the user fills `mirror-values.yaml` by hand for those.
