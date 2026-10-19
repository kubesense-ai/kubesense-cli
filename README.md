# Installation Guide

## Prerequisites

Before you begin the installation, ensure you have the following:

- A Unix-based operating system (Linux, macOS, etc.)
- Access to the terminal or command line interface
- Required permissions to execute scripts

## Installation Steps

Follow these steps to install the <strong>kubesense</strong> binary using the provided shell script:

1. **Download and execute the installation script**
   
   You can do this via `curl` or `wget`:
   ```bash
   curl -s -L https://cli.kubesense.ai/install.sh | bash
   ```
   
   ```bash
   wget -O - https://cli.kubesense.ai/install.sh | bash
   ```
## Review a change before you install or upgrade: `plan` and `show`

`kubesense plan` shows what an install or upgrade would change, like `terraform
plan`, **without changing anything in the cluster**. It renders the chart with
your values against the live cluster the way the real run would (a Helm
server-side dry run, so lookups see the cluster), then lists every value and
resource that would change.

```bash
# 1. Start from the overrides the release was installed with (not merged
#    defaults: upgrading from those would pin the old chart's defaults).
kubesense values pull -n kubesense --release kubesense -o overrides.yaml

# 2. Edit overrides.yaml, then see what the upgrade would do.
kubesense plan --chart kubesense-server --version 2.3.9 -f overrides.yaml

# Several values files merge left to right, like helm -f. A local chart works too.
kubesense plan --chart ./charts/kubesense-server -f base.yaml -f prod.yaml --out upgrade.plan
kubesense show upgrade.plan
```

The output names the cluster (kubeconfig context, API server and the
`kube-system` UID), then the overrides that change, then each resource to add
(`+`), change (`~`) or remove (`-`), with the fields that differ:

```
Cluster:  prod-eks (https://ABC.gr7.eu-west-1.eks.amazonaws.com)
          cluster ID 916c0bdf-... (kube-system UID)
Target:   namespace kubesense, release kubesense (upgrade)
Baseline: managed, revision 6 (deployed) -> 7
...
Overrides:
  ~ api.replicaCount: 3 -> 4
  ~ global.externalMySQL.password: (sensitive value changed)

Resources:
  ~ Deployment/kubesense-api
        spec.replicas: 3 -> 4

Plan: 0 to add, 1 to change, 0 to destroy.
```

* **Read-only.** The connection refuses every write; the plan cannot change the cluster.
* **Credentials are masked**: Secret data, and any value whose key looks like a
  password, token, key or certificate. A plan saved with `--out` keeps your
  overrides, so it is written readable only by you; keep it out of Git.
* **Only what the action runs**: install-only hooks are not listed on an upgrade.
* **`--detailed-exitcode`** exits 0 with no changes, 2 with changes, 1 on error, for CI.
* **Kubeconfig**: `--kubeconfig` and `--context`, or kubectl's defaults. Check the
  `Cluster:` line before you act on a plan.
* **Warnings** you may see: the KubeSense chart generates some passwords and
  certificates while rendering, so those can differ between the plan and the
  real run; and for Argo CD / Flux installs, values change in Git, so the plan is
  for review only.

This release ships `plan` and `show` only. Apply the change the way you do
today (`helm upgrade` with the same values files, or Git for Argo CD / Flux).
