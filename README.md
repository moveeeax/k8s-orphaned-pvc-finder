# k8s-orphaned-pvc-finder

[![ci](https://github.com/moveeeax/k8s-orphaned-pvc-finder/actions/workflows/ci.yml/badge.svg)](https://github.com/moveeeax/k8s-orphaned-pvc-finder/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/go-1.22%2B-00ADD8?logo=go)](https://go.dev)
[![license](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

Find Kubernetes **PersistentVolumeClaims that no Pod references** — the quiet ones
still holding a billed EBS/PD volume long after the workload that created them is gone.
StatefulSet PVCs in particular are left behind *by design*, and there is no built-in
`kubectl` view that answers "which PVCs are bound but mounted by nothing, and how much
storage is that?". This CLI does.

## How it works

`k8s-orphaned-pvc-finder` lists every PVC and every Pod (live via your kubeconfig, or
from an offline dump), builds the set of claims any Pod mounts through
`spec.volumes[].persistentVolumeClaim`, and flags every PVC *not* in that set as
**orphaned**. Matching is namespace-scoped: a claim of the same name in another
namespace never rescues an orphan.

The classifier is a pure function over PVC and Pod lists, so it runs entirely offline
against a dump and is fully unit-tested with no API server.

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

# One namespace, JSON for a dashboard
k8s-orphaned-pvc-finder scan -n data --output json > orphans.json

# Gate CI: exit non-zero if any orphaned PVC exists
k8s-orphaned-pvc-finder scan --all-namespaces --fail-on orphans
```

### Offline mode (no cluster)

Feed it a dump of PVCs and Pods — handy for CI, audits, or a cluster you can only
export from:

```sh
kubectl get pvc -A -o json | jq '{pvcs: .items}'  > pvcs.json
kubectl get pods -A -o json | jq '{pods: .items}'  > pods.json
jq -s '.[0] * .[1]' pvcs.json pods.json > dump.json

k8s-orphaned-pvc-finder scan --input dump.json
```

Example run against the bundled fixture:

```console
$ k8s-orphaned-pvc-finder scan --input examples/cluster-dump.json
NAMESPACE  NAME            PHASE  STORAGECLASS  CAPACITY  AGE
data       leftover-batch  Bound  io2           100Gi     298d
data       no-class-vol    Bound  <none>        1Gi       282d
shop       data-cache-0    Bound  gp3           5Gi       220d

3 orphaned PVC(s), 106.00 GiB wasted total
  <none>               1.00 GiB
  gp3                  5.00 GiB
  io2                  100.00 GiB
```

## Flags

| Flag | Description |
| --- | --- |
| `-n, --namespace` | Namespace to scan (default: current context namespace) |
| `-A, --all-namespaces` | Scan every namespace |
| `-o, --output` | Output format: `table` (default) or `json` |
| `--input` | Read an offline PVC+Pod dump (JSON/YAML) instead of a live cluster |
| `--fail-on orphans` | Exit non-zero when any orphan is found (for CI) |
| `--kubeconfig` | Path to kubeconfig (default: `$KUBECONFIG` or `~/.kube/config`) |

## Exit codes

| Code | Meaning |
| --- | --- |
| `0` | Scan succeeded (with `--fail-on`, also means no orphans) |
| `1` | `--fail-on` matched: orphans were found |
| `2` | Usage or runtime error |

## License

MIT — see [LICENSE](LICENSE).
