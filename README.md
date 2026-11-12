# k8s-orphaned-pvc-finder

[![ci](https://github.com/moveeeax/k8s-orphaned-pvc-finder/actions/workflows/ci.yml/badge.svg)](https://github.com/moveeeax/k8s-orphaned-pvc-finder/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/go-1.26%2B-00ADD8?logo=go)](https://go.dev)
[![license](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

Find Kubernetes **PersistentVolumeClaims that no Pod references** — the quiet ones
still holding a billed EBS/PD volume long after the workload that created them is gone.
StatefulSet PVCs in particular are left behind *by design*, and there is no built-in
`kubectl` view that answers "which PVCs are bound but mounted by nothing, and how much
storage is that?". This CLI does.

## How it works

`k8s-orphaned-pvc-finder` lists every PVC, Pod and StatefulSet (live via your
kubeconfig, or from an offline dump), builds the set of claims any active Pod mounts
through `spec.volumes[].persistentVolumeClaim`, and flags every PVC *not* in that set
as **orphaned**. Matching is namespace-scoped: a claim of the same name in another
namespace never rescues an orphan.

- **Finished Pods don't count.** A Job or CronJob leaves `Succeeded`/`Failed` Pods
  that still list their claim; by default only Pods that are not finished (`Pending`,
  `Running`, `Unknown`) keep a PVC in use. `--count-pods all` counts every Pod.
- **Live StatefulSets keep their claims.** A PVC named
  `<volumeClaimTemplate>-<statefulset>-<ordinal>` whose StatefulSet still exists (for
  example a scaled-down replica) is skipped and only counted in the summary. Once the
  StatefulSet is deleted its claims are reported. If the scan may not list
  StatefulSets, it warns and reports those claims as orphans.
- **Every orphan carries a reason:** `unreferenced` (no Pod lists it),
  `finished-only` (only finished Pods list it; the newest one is shown as
  `lastUsedBy`) or `never-bound` (the claim is `Pending`, so no volume was ever
  bound or mounted). Kubernetes keeps no mount history, so an `unreferenced` claim
  may be one that was never mounted or one whose workload was deleted.

The classifier is a pure function over PVC, Pod and StatefulSet lists, so it runs
entirely offline against a dump and is fully unit-tested with no API server.

## Install

```sh
go install github.com/moveeeax/k8s-orphaned-pvc-finder/cmd/k8s-orphaned-pvc-finder@latest
```

Or build from source:

```sh
git clone https://github.com/moveeeax/k8s-orphaned-pvc-finder.git
cd k8s-orphaned-pvc-finder
go build -o k8s-orphaned-pvc-finder ./cmd/k8s-orphaned-pvc-finder
```

## Usage

```sh
# Scan every namespace in the current kube-context
k8s-orphaned-pvc-finder scan --all-namespaces

# Two namespaces, JSON for a dashboard
k8s-orphaned-pvc-finder scan -n data,shop --output json > orphans.json

# Monthly cost report: claims older than 30 days, as CSV
k8s-orphaned-pvc-finder scan -A --min-age 30d --price-file prices.yaml --output csv

# Gate CI: exit non-zero if any orphaned PVC exists
k8s-orphaned-pvc-finder scan --all-namespaces --fail-on orphans
```

### Offline mode (no cluster)

Feed it a dump of PVCs, Pods and (optionally) StatefulSets — handy for CI, audits,
or a cluster you can only export from:

```sh
kubectl get pvc -A -o json | jq '{pvcs: .items}'          > pvcs.json
kubectl get pods -A -o json | jq '{pods: .items}'         > pods.json
kubectl get sts -A -o json | jq '{statefulsets: .items}'  > sts.json
jq -s '.[0] * .[1] * .[2]' pvcs.json pods.json sts.json > dump.json

k8s-orphaned-pvc-finder scan --input dump.json
```

Without `statefulsets` in the dump no claim is skipped as StatefulSet-owned. With
`--input`, `-n` filters the dump; without it the whole dump is scanned.

Example run against the bundled fixture:

```console
$ k8s-orphaned-pvc-finder scan --input examples/cluster-dump.json --price-file examples/prices.yaml
NAMESPACE  NAME            PHASE  STORAGECLASS  ACCESS  CAPACITY  AGE   REASON                                         COST/MO
data       leftover-batch  Bound  io2           RWO     100Gi     362d  unreferenced                                   12.50 USD
data       nightly-export  Bound  gp3           RWO     50Gi      256d  finished-only (nightly-export-29170080-x7k2p)  4.00 USD
data       no-class-vol    Bound  <none>        RWO     1Gi       346d  unreferenced                                   0.10 USD
shop       data-cache-0    Bound  gp3           RWO     5Gi       284d  unreferenced                                   0.40 USD

4 orphaned PVC(s), 156.00 GiB wasted total, 17.00 USD/month
  <none>               1.00 GiB     0.10 USD/month
  gp3                  55.00 GiB    4.40 USD/month
  io2                  100.00 GiB   12.50 USD/month

1 unreferenced PVC(s) skipped: owned by a live StatefulSet volumeClaimTemplate
```

### Cost estimates

There is no built-in price list: storage prices differ by provider and region and
change. Give your own, in currency per GiB-month:

```yaml
# prices.yaml
currency: USD
default: 0.10        # optional: any class not listed below
classes:
  gp3: 0.08
  io2: 0.125
  "<none>": 0.10     # claims with no storage class
```

`--price CLASS=PRICE` (repeatable) sets or overrides one class, `--default-price`
sets the fallback and `--currency` the label, so a price file is optional. The
fallback is a separate field, not a class named `default`, because `default` is a
real StorageClass name on some clusters.

Cost is `capacity in GiB × price`, per claim, rounded to cents. A claim in a class
with no price shows `?` in the table, gets no `monthlyCost` in JSON and an empty
cell in CSV, is left out of the totals, and its class is printed as a warning on
stderr and listed in `unpricedStorageClasses`. It is never counted as zero.

JSON gains `monthlyCost` per orphan plus `currency`, `totalMonthlyCost`,
`monthlyCostByStorageClass` and `unpricedStorageClasses`; these keys are absent
when no prices are given.

## Flags

| Flag | Description |
| --- | --- |
| `-n, --namespace` | Namespace(s) to scan, repeatable or comma-separated (default: current context namespace; with `--input`: the whole dump) |
| `-A, --all-namespaces` | Scan every namespace |
| `-o, --output` | Output format: `table` (default), `json` or `csv` |
| `--input` | Read an offline PVC+Pod(+StatefulSet) dump (JSON/YAML) instead of a live cluster |
| `--count-pods` | Which Pods keep a PVC in use: `running` (default; everything but `Succeeded`/`Failed`) or `all` |
| `--min-age` | Only report PVCs at least this old, e.g. `30d`, `12h` |
| `--price-file` | YAML/JSON price table for cost estimates (see above) |
| `--price CLASS=PRICE` | Price per GiB-month for one storage class; repeatable, overrides the file |
| `--default-price` | Price per GiB-month for every class without its own price |
| `--currency` | Currency label for costs (overrides the file's `currency`) |
| `--fail-on orphans` | Exit non-zero when any orphan is found (for CI) |
| `--kubeconfig` | Path to kubeconfig (default: `$KUBECONFIG` or `~/.kube/config`) |

### RBAC

A live scan needs `list` on `persistentvolumeclaims` and `pods` (core) and
`statefulsets` (`apps`) in the scanned namespaces.

## Exit codes

| Code | Meaning |
| --- | --- |
| `0` | Scan succeeded (with `--fail-on`, also means no orphans) |
| `1` | `--fail-on` matched: orphans were found |
| `2` | Usage or runtime error |

## License

MIT — see [LICENSE](LICENSE).
