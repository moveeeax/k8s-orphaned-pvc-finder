// Command k8s-orphaned-pvc-finder scans a cluster (or an offline dump) for
// PersistentVolumeClaims that no Pod references, so you can reclaim the storage.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/moveeeax/k8s-orphaned-pvc-finder/internal/finder"
	"github.com/moveeeax/k8s-orphaned-pvc-finder/internal/kube"
	"github.com/moveeeax/k8s-orphaned-pvc-finder/internal/report"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := newRootCmd().Execute(); err != nil {
		var ee *exitError
		if errors.As(err, &ee) {
			os.Exit(ee.code)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "k8s-orphaned-pvc-finder",
		Short:         "Find orphaned Kubernetes PersistentVolumeClaims",
		Long:          "Find PersistentVolumeClaims that no Pod references, so you can reclaim wasted storage.",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newScanCmd())
	return root
}

func newScanCmd() *cobra.Command {
	var (
		namespace     string
		allNamespaces bool
		output        string
		input         string
		failOn        string
		kubeconfig    string
	)

	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Scan for orphaned PVCs",
		RunE: func(cmd *cobra.Command, args []string) error {
			if output != "table" && output != "json" {
				return fmt.Errorf("--output must be 'table' or 'json', got %q", output)
			}
			if failOn != "" && failOn != "orphans" {
				return fmt.Errorf("--fail-on only supports 'orphans', got %q", failOn)
			}

			pvcs, pods, err := load(cmd.Context(), input, namespace, allNamespaces, kubeconfig)
			if err != nil {
				return err
			}

			res := finder.Classify(pvcs, pods)

			out := cmd.OutOrStdout()
			if output == "json" {
				if err := report.JSON(out, res); err != nil {
					return err
				}
			} else if err := report.Table(out, res, time.Now()); err != nil {
				return err
			}

			if failOn == "orphans" && len(res.Orphans) > 0 {
				return &exitError{code: 1}
			}
			return nil
		},
	}

	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "namespace to scan (default: current context namespace)")
	cmd.Flags().BoolVarP(&allNamespaces, "all-namespaces", "A", false, "scan every namespace")
	cmd.Flags().StringVarP(&output, "output", "o", "table", "output format: table | json")
	cmd.Flags().StringVar(&input, "input", "", "read an offline PVC+Pod dump (JSON/YAML) instead of a live cluster")
	cmd.Flags().StringVar(&failOn, "fail-on", "", "exit non-zero on findings: orphans")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig (default: $KUBECONFIG or ~/.kube/config)")
	return cmd
}

// load returns PVCs and Pods either from an offline dump or a live cluster.
func load(ctx context.Context, input, namespace string, allNamespaces bool, kubeconfig string) ([]finder.PVC, []finder.Pod, error) {
	if input != "" {
		data, err := os.ReadFile(input)
		if err != nil {
			return nil, nil, fmt.Errorf("read input: %w", err)
		}
		return finder.ParseDump(data)
	}

	cs, err := kube.NewClientset(kubeconfig)
	if err != nil {
		return nil, nil, err
	}
	ns := namespace
	if allNamespaces {
		ns = ""
	}
	return kube.Fetch(ctx, cs, ns)
}

// exitError carries a specific process exit code up to main without printing a
// noisy "error:" line — used for the --fail-on CI gate.
type exitError struct{ code int }

func (e *exitError) Error() string { return fmt.Sprintf("exit %d", e.code) }
