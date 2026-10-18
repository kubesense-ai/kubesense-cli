# Spike: plan / show / apply on the embedded Helm SDK (v3.16.1)

Spec: `kube-devops/CONTROL-PLANE-DESIGN.md` §3.5 ("The invariant", "Known gaps in
Helm v3.16.1", "Spike acceptance criterion").
Branch: `spike/plan-apply`. Code: `pkg/plan/`, `cmd/planapply.go`.
Date: 2026-10-18.

## TL;DR

* **The invariant can be met with Helm v3.16.1 without forking or patching Helm**,
  for both install and upgrade, by using option **(b)**: wrap Helm's release
  storage driver, then validate the release object Helm is about to persist
  (it carries the final post-rendered manifests and the final hooks of the real
  execution), plus a fresh release-baseline and live-baseline recheck, inside
  the driver's `Create`. That call is Helm's first write. A write barrier
  around Helm's kube client refuses any mutation before that point, so the
  ordering is enforced by code rather than assumed from reading Helm.
* Every acceptance case passes for install and upgrade, and refusals are
  checked against the cluster: `resourceVersion` snapshots of every Secret (incl.
  `sh.helm.release.v1.*`), ConfigMap, Deployment and CRD, taken before and after.
* **A post-renderer by itself does not meet the invariant.** A hook-only change
  gets applied. This is demonstrated by a test, not just inferred.
* **Option (a), the pure-render lint, is not needed for safety but is needed for
  usability.** Without it, a chart that uses `randAlphaNum`/`genCA` can never be
  applied, because every render differs. **The real KubeSense chart fails the
  lint badly** (23 files call `lookup`, 16 call `randAlphaNum`, 9 call `genCA`), and
  its `kubesense-secret` pre-install hook renders differently each time. So a
  fresh install of today's chart would always be refused. This needs a chart
  decision (see Decisions).
* The guarantee holds **up to the first write**. After the release record is
  written there is a residual window. Pre-hooks run there, and then Helm
  patches resources against whatever is live at that moment. Details under
  Residual gaps.
* Upgrading to the latest Helm v3 (v3.22.1) **changes nothing** here. The write
  order and `renderResources` are the same; only the line numbers move by 3.

## 1. Helm v3.16.1 write ordering (source: `$GOMODCACHE/helm.sh/helm/v3@v3.16.1`)

### Install — `pkg/action/install.go` `RunWithContext`

| Line | Step | Write? |
|---|---|---|
| 235 | `KubeClient.IsReachable` | read |
| 245 | `availableName` (release name not in use) | read |
| 249 | `ProcessDependenciesWithMerge` | — |
| **260–267** | **`crds/` objects → `installCRDs` (:160, `KubeClient.Create` at :171)** | **WRITE, before render** |
| 290 | `getCapabilities` (uses `cfg.Capabilities` as-is if non-nil, `action.go:247`) | read |
| 316 | `renderResources` → `action.go:134` `engine.Render` (**`lookup` GETs happen here**), `:168` `SortManifests` (**hooks split off**), `:225-226` **post-renderer, non-hook manifests only** | read |
| 322–326 | render / post-renderer error → return | — |
| 332, 349–358 | `KubeClient.Build`, `existingResourceConflict` | read |
| 361 | dry-run bail-out | — |
| 366–390 | `CreateNamespace` → `KubeClient.Create` (:387) | **WRITE** (only if set) |
| 393–397 | `Replace` → `replaceRelease` | **WRITE** (only if set) |
| **401** | **`Releases.Create(rel)`, `pending-install` record** | **first WRITE in the normal path** |
| 408 → 447 | pre-install hooks (`hooks.go:55` delete by policy, `:77` `KubeClient.Create`) | WRITE |
| 456 / 458 | `KubeClient.Create(resources)` / `Update` (adoption) | WRITE |
| 476, 494 | post-install hooks, `recordRelease` | WRITE |

### Upgrade — `pkg/action/upgrade.go` `RunWithContext`

| Line | Step | Write? |
|---|---|---|
| 152 | `IsReachable` | read |
| 165 → 209 | `prepareUpgrade`: `Releases.Last` | read |
| 219 | last release pending → `errPending` (Helm's own lock) | — |
| 241 | `reuseValues` (spike sets `ResetValues=true`, so the plan's overrides are authoritative) | — |
| 261 | `getCapabilities` | read |
| 276 | `renderResources` (post-renderer inside, as above) | read |
| 277–278 | render / post-renderer error → return | — |
| 306 | `validateManifest` | read |
| 170 | `Releases.MaxHistory = u.MaxHistory` | — |
| 311–354 | `Build` current/target, `existingResourceConflict` | read |
| 365 | dry-run bail-out | — |
| **376** | **`Releases.Create(upgradedRelease)`, `pending-upgrade` record.** If `MaxHistory > 0`, `storage.go:62-67` `removeLeastRecent` **deletes old records first** | **first WRITE** |
| 383 → 421 | pre-upgrade hooks | WRITE |
| 429 | `KubeClient.Update(current, target)`: `kube/client.go:678` builds a 3-way patch from the object it reads live at that moment, and `:694` sends `Patch` **without a resourceVersion precondition** | WRITE |
| 181 | `Releases.Update` (final status) | WRITE |

Upgrade **never** installs `crds/` (no CRD path in `upgrade.go`).

### Answers to the spike's questions

* **Does a PostRenderer error happen before any write?** Upgrade: **yes**, always
  (`:276` comes before `:376`). Install: **yes, unless the chart has `crds/`**, which are
  created at `:264` before rendering. Nothing else writes before `:316` if
  `CreateNamespace`/`Replace` are off.
* **Can a PostRenderer see hooks?** **No.** Hooks are split off at `action.go:168`, before
  `:225`. Demonstrated by `TestGap_PostRendererOnlyMissesHookChange`, which applies an
  unplanned hook.
* **Where is the true execution boundary?** `Releases.Create` (install `:401`, upgrade
  `:376`). The object it receives is exactly what Helm then executes: `rel.Manifest` is
  what goes into `KubeClient.Build` before the Create, and `rel.Hooks` is what
  `execHook` iterates. Nothing re-renders after it.
* **Latest Helm v3 (v3.22.1, downloaded to the module cache and read; not adopted):** same
  ordering. `install.go:267` installCRDs, `:319` renderResources, `:404` Releases.Create;
  `upgrade.go:276`, `:376`. `action.go` renderResources is identical apart from a comment.
  `storage.Create` still prunes before Create. Upgrading neither closes nor opens any gap.
  Helm v4 was not investigated.

## 2. What was built

`pkg/plan`:

* `MakePlan` loads the chart and **rejects `crds/` at any level**, runs the pure-render
  lint, and connects through an **explicit kubeconfig** (the spike refuses any non-loopback
  server). It reads Capabilities from discovery and pins them, then determines the
  baseline (**fresh / adopted / managed / gitops**; managed means the
  `kubesense-deployment-info` Secret exists). It renders with a Helm **server-side
  dry-run**, so `lookup` behaves as it will in the real run. It records:
  * the chart digest over every loaded file, recursively including dependencies
  * the overrides, round-tripped through JSON so plan and apply use identical types
  * a Capabilities hash
  * a digest of all release records (rev, status, chart, manifest, hooks, config) and the deployment-info hash
  * a live hash of every touched object: the new manifests and hooks, the current release's manifests and hooks, and deployment-info. The hash covers normalized configuration and drops `status`, `resourceVersion`, `managedFields`, `uid`, `creationTimestamp`, `generation` and `ownerReferences`. Absent objects are pinned as absent.
  * the manifest hash, the hooks hash (content, events, weight, delete policy), the CLI and Helm versions, the creation time and the expiry

  Plan connections carry a gate that never opens, so `plan` provably cannot write
  (`TestPlanIsReadOnly`).
* `Plan.Show` prints the saved plan: values diff, then `+`/`~`/`-` per object with
  field-level detail. It never recomputes.
* `Apply` runs in this order:
  1. **Prechecks:** speculative, expiry, CLI version, Helm version, server URL, chart
     digest, `crds/`, lint, Capabilities (then pinned into `cfg.Capabilities`, so the
     real render uses the verified value), release baseline, deployment-info, live
     baseline.
  2. **Real Helm install/upgrade** with `CreateNamespace=false`, `MaxHistory=0` and
     `ResetValues=true`.
  3. **Post-renderer:** manifest hash.
  4. **Gate** in the wrapped storage driver's `Create`: candidate revision, manifest
     hash, **hooks hash**, release-baseline recheck, deployment-info recheck,
     live-baseline recheck.
  5. **Write barrier**: `*kube.Client` is embedded, so every optional interface Helm
     type-asserts for is still there. Create/Update/Delete fail until the gate opens.
* `cmd/planapply.go`: `kubesense plan|show|apply` (`--kubeconfig` is required). There
  is an end-to-end smoke test: plan → show → apply → re-apply refused.

Tests run against **controller-runtime envtest** (kube-apiserver + etcd v1.31.0) started in
`TestMain`. The kubeconfig is written to a temp dir, `KUBECONFIG` points at it, and the
loopback assertion runs before any test. The default kubeconfig is never read.

## 3. Acceptance matrix

All rows are passing tests (`go test ./...`, 2026-10-18). "No write" means the
before/after resourceVersion snapshots are identical, release records included.

| Case | install | upgrade | Test | Caught at |
|---|---|---|---|---|
| Ordinary manifest change (chart file edited) | PASS | PASS | `TestAccept_ManifestChange_ChartDigest` | precheck: chart-digest |
| …same, digest check bypassed | PASS | PASS | `TestAccept_ManifestChange_RenderCheck` | post-render (real render) |
| Hook-only change | PASS | PASS | `TestAccept_HookOnlyChange/*/chart-digest` | precheck: chart-digest |
| …same, digest check bypassed | PASS | PASS | `TestAccept_HookOnlyChange/*/execution-boundary` | **gate: rendered-hooks** |
| Lookup chart, lint on | PASS | PASS | `TestAccept_LookupInputChange/*/lint-rejects-at-plan` | plan refuses (LintError) |
| Lookup input change → manifest, **no lint** | PASS | PASS | `…/no-lint/manifest-lookup` | post-render |
| Lookup input change → hook, **no lint** | PASS | PASS | `…/no-lint/hook-lookup` | **gate: rendered-hooks** |
| Lookup chart, input unchanged, no lint → applies | PASS | PASS | `…/no-lint/unchanged-input-applies` | — |
| Live baseline: manual change before apply | PASS | PASS | `TestAccept_LiveBaselineChange/*/before-apply` | precheck: live-baseline |
| Live baseline: change injected **after** prechecks | PASS | PASS | `…/after-precheck` | **gate: live-baseline** |
| Release baseline: other install/upgrade before apply | PASS | PASS | `TestAccept_ReleaseBaselineChange/*/before-apply` | precheck: release-baseline |
| Release baseline: change injected after prechecks | PASS¹ | PASS | `…/after-precheck` | install: Helm `availableName` (read); upgrade: **gate: revision** |
| deployment-info edited | n/a | PASS | `…/upgrade/deployment-info-edited` | precheck |
| `crds/` in chart: plan | PASS | PASS | `TestAccept_ChartWithCRDs/*/top-level/plan-rejects` | plan refuses |
| `crds/` in chart: added after plan | PASS | PASS | `…/top-level/apply-rejects` | precheck: crds |
| `crds/` in a dependency: plan / apply | PASS | PASS | `…/dependency/{plan,apply}-rejects` | plan / precheck |
| `crds/` with the CRD check also bypassed | PASS | n/a² | `…/install/barrier-blocks-installCRDs` | **write barrier** stops `installCRDs` |
| **Positive:** unchanged inputs apply; the stored record's manifest and hooks hashes equal the plan's | PASS | PASS | `TestAccept_Positive` | — |
| **Gap demo:** post-renderer only, hook change | applied (gap) | applied (gap) | `TestGap_PostRendererOnlyMissesHookChange` | not caught (expected) |
| Impure chart (`randAlphaNum`), no lint | refuses | refuses | `TestImpureChartWithoutLintNeverApplies` | post-render, every time |
| Expired / CLI version / Helm version / speculative / other cluster / Capabilities | — | PASS³ | `TestApplyRefusesBadPlanFiles` | precheck |
| Plan writes nothing | PASS | PASS | `TestPlanIsReadOnly` | — |
| Adopted → plan shows `+ Secret/kubesense-deployment-info` → managed | n/a | PASS | `TestAdoptedBaseline` | — |
| GitOps metadata → plan refuses | n/a | PASS | `TestGitOpsBaselineRefused` | — |
| Non-loopback kubeconfig / no kubeconfig refused | PASS | PASS | `TestSafety_RefusesNonLoopbackServer` | — |
| CLI plan → show → apply → re-apply refused | PASS | — | `cmd/TestPlanShowApplyCommands` | — |

¹ Helm's own name check fires before our gate. It is a read, and no write happened.
² Helm upgrade never installs `crds/`.
³ These are mode-independent prechecks and were run on an upgrade plan only.

A live-baseline detail found along the way: if someone creates an object the install
will create, Helm's own `existingResourceConflict` (install.go:353) refuses it, unless the
object carries this release's Helm ownership labels and annotations. In that case Helm
would **silently adopt and overwrite** it, and only the live-baseline pin catches it. The
test uses that variant.

**Residual window, measured** (`TestAccept_Positive`, envtest, 4 pinned objects):
boundary validation took about 5–6 ms (release-history read plus 4 sequential GETs).
From the end of validation to the first release-record write took about 40–125 ns,
with **0 API calls** in between.

## 4. Which option achieves the invariant

| | Manifests | Hooks | Lookup | Live baseline at boundary | Real chart usable? |
|---|---|---|---|---|---|
| (a) lint + post-renderer + digest | yes | yes **by inference only**: correct only if the deny-list is complete; not tested in that configuration | banned | possible in the post-renderer, but that runs before Build/conflict checks | **no**: today's chart fails the lint |
| (b) storage-driver gate + barrier | **yes (tested)** | **yes (tested)** | **yes (tested)**: the actual output is compared | **yes (tested)** | install: no, because the hooks are random; upgrade: likely (lookup-or-reuse is stable), not tested |
| (b) post-renderer only | yes | **no (tested gap)** | manifests only | — | — |

**Verdict: (b), the storage-driver gate with the write barrier, is what achieves the
invariant.** (a) adds two things. Charts become *appliable*, because output is
deterministic. And a mismatch is caught at plan time, not as an apply that always
refuses. Use both, but treat (a) as a chart-quality gate, not the safety mechanism.

## 5. Residual gaps (honest list)

1. **After the boundary the guarantee does not hold, by design.** The invariant is "refuse
   before any write", and it holds. But between the release-record write and the resource
   writes Helm runs **pre-install/pre-upgrade hooks**. For KubeSense these can be
   migration Jobs taking minutes. Then `KubeClient.Update` 3-way-patches against the
   object it reads live at that moment, with no resourceVersion precondition
   (`kube/client.go:678-694`). A manual `kubectl patch` made in that window is merged
   or overwritten without being detected. Closing it needs a custom `Update` in the
   barrier that rechecks each object's hash immediately before patching and sends
   `metadata.resourceVersion` as an optimistic-concurrency precondition. That is
   feasible because we already wrap the client, but it is **not implemented or
   tested**. Even then there is no cross-object transaction.
2. **The live recheck is sequential.** The window for the first-checked object is the
   whole recheck (about 5 ms for 4 objects here; a guess of 0.5–1 s for KubeSense's
   100+ objects). It could be parallelized.
3. **False refusals on real clusters are untested.** envtest has no controllers. HPA-owned
   `spec.replicas`, `deployment.kubernetes.io/revision`, webhook `caBundle` injection and
   similar fields will change live hashes without any human drift. We need per-kind
   ignore rules, validated on kind/EKS with controllers running.
4. **Objects read by `lookup` are not pinned.** A change to one is caught only if it
   changes rendered output, which is the correct semantics. Objects that matter at runtime
   but not at render time (StorageClasses, external Secrets) are not covered.
5. **The lint is a deny-list**: it is conservative and noisy. `tpl` and value strings
   containing `{{` are flagged, and KubeSense's Vector configs legitimately contain `{{ }}`.
   For the real chart that is 272 value strings and 144 `tpl` files. Under (b) the lint is
   not load-bearing for safety.
6. **The real chart renders differently each time** (client-only double render, read
   only):
   * `kubesense-server/templates/hooks/pre-install/db-password-secret.yaml` (the
     `kubesense-secret` hook) generates passwords on each install render.
   * With `AKS/dev.yaml`, `api/saml-secret.yaml` differs too.
   * `genCA`/`genSignedCert` are used in the datastore/zookeeper TLS secrets.

   Under (b), any of these on the rendered path means **apply always refuses**. This was
   not tested against a live cluster holding pre-existing secrets, where lookup-or-reuse
   should be stable.
7. **Preconditions the spike enforces by configuration:**
   * `CreateNamespace` off: it writes before the record, so the namespace must already
     exist.
   * `MaxHistory=0`: Helm prunes records *before* the gated Create, so pruning has to
     run after apply.
   * `Replace`, `TakeOwnership` and `Atomic` are not used.

   If a later change turns any of them on, the barrier fails closed.
8. **The plan file is not signed.** Anyone who can edit it can recompute the hashes. It
   also contains the overrides, including credentials (written 0600). Cluster identity
   is checked by API server URL only; production should use the `kube-system` namespace
   UID.
9. **Spike-only knobs** (`AllowImpureTemplates`, `skipDigestCheck`, `skipCRDCheck`,
   `gateOff`, `beforeExecute`) exist to isolate each layer in tests. They must not ship.
   All of them except `AllowImpureTemplates` are unexported.
10. **Only the gate is pinned to Helm internals.** It relies on public API (the
    `driver.Driver` interface and embedding `*kube.Client`), but its correctness
    assumes "the first `Releases.Create` carries the final release". Every Helm bump
    must re-run this suite. The barrier makes a regression fail closed, not open.

## 6. Recommended production design

* Keep the layering from the spike: **precheck → pinned Capabilities → real Helm
  run → post-renderer (cheap early manifest check) → storage-driver gate (manifests,
  hooks, release baseline, live baseline) → write barrier.**
* Extend the barrier's `Update`/`Create` to do **per-object CAS**: recheck the hash,
  then patch with a `resourceVersion` precondition. This narrows gap 1 to a single
  GET→PATCH per object. Run the live recheck in parallel.
* Normalization rules per kind (ignore controller-owned fields), tested on kind with
  controllers.
* Chart work, a prerequisite for installs:
  * `secrets init` owns `kubesense-secret` and the SAML secret
  * remove the random generation from hooks and templates
  * replace `genCA` TLS with pre-provisioned certificates or cert-manager
  * keep `lookup` only in lookup-or-reuse form

  Ship the lint as CI on the chart repo with an allowlist, and in `plan` as a warning,
  not a hard error.
* Plan files: sign them (or register the plan digest with the manager); identify the
  cluster by the `kube-system` UID; set an expiry.
* Stay on Helm v3.16.x, or bump freely within v3. Pin the version and re-run this suite on
  every bump.

## 7. Effort estimate (production, one engineer; rough)

| Item | Weeks |
|---|---|
| Plan engine hardening: embedded/registry chart, `values pull/effective`, diff library, shared preflight rules, `render` | 2–3 |
| Gate + barrier hardening: per-object CAS, parallel recheck, per-kind normalization on real clusters | 1.5–2 |
| Chart determinism (secrets init, remove rand/genCA, lint in CI) and migration for existing installs | 1–2 (chart owner) |
| Plan signing / manager registration, cluster identity | 0.5–1 |
| E2E matrix on kind with the real chart (install, upgrade, adopted) | 1 |
| **Total** | **≈ 6–9** |

## 8. Decisions needed

1. **Adopt (b) as the guarantee mechanism, with (a) as an advisory chart lint**, instead
   of making a strict (a) the gate. The real chart fails strict (a) by a wide margin.
2. **Chart determinism work is a prerequisite for `apply` on fresh installs.** Until it
   lands, every fresh install of today's chart is refused.
3. **Is the post-record window acceptable** (hooks run, then non-CAS patches), or do we
   fund per-object CAS before claiming the guarantee?
4. **Plan-file signing**, and where the approval record lives.
5. **Namespace and history pruning move outside the Helm call** (both write before the
   record otherwise).
6. **Helm upgrade is not needed** for this design (v3.22.1 has the same ordering).
