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
	"github.com/moveeeax/k8s-orphaned-pvc-finder/internal/pricing"
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

// scanFlags holds the scan command's flag values.
type scanFlags struct {
	namespaces    []string
	allNamespaces bool
	output        string
	input         string
	failOn        string
	kubeconfig    string
	countPods     string
	minAge        string
	priceFile     string
	prices        []string
	defaultPrice  string
	currency      string
}

func newScanCmd() *cobra.Command {
	var f scanFlags

	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Scan for orphaned PVCs",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runScan(cmd, f)
		},
	}

	cmd.Flags().StringSliceVarP(&f.namespaces, "namespace", "n", nil, "namespace(s) to scan, repeatable or comma-separated (default: current context namespace; with --input: the whole dump)")
	cmd.Flags().BoolVarP(&f.allNamespaces, "all-namespaces", "A", false, "scan every namespace")
	cmd.Flags().StringVarP(&f.output, "output", "o", "table", "output format: table | json | csv")
	cmd.Flags().StringVar(&f.input, "input", "", "read an offline PVC+Pod(+StatefulSet) dump (JSON/YAML) instead of a live cluster")
	cmd.Flags().StringVar(&f.failOn, "fail-on", "", "exit non-zero on findings: orphans")
	cmd.Flags().StringVar(&f.kubeconfig, "kubeconfig", "", "path to kubeconfig (default: $KUBECONFIG or ~/.kube/config)")
	cmd.Flags().StringVar(&f.countPods, "count-pods", string(finder.CountRunning), "which Pods keep a PVC in use: running (not Succeeded/Failed) | all")
	cmd.Flags().StringVar(&f.minAge, "min-age", "", "only report PVCs at least this old, e.g. 30d or 12h")
	cmd.Flags().StringVar(&f.priceFile, "price-file", "", "YAML/JSON price table (currency per GiB-month per storage class) for cost estimates")
	cmd.Flags().StringArrayVar(&f.prices, "price", nil, "price per GiB-month for one storage class, CLASS=PRICE (repeatable, overrides --price-file)")
	cmd.Flags().StringVar(&f.defaultPrice, "default-price", "", "price per GiB-month for every storage class without its own price")
	cmd.Flags().StringVar(&f.currency, "currency", "", "currency label for cost estimates (overrides the price file's currency)")
	return cmd
}

func runScan(cmd *cobra.Command, f scanFlags) error {
	if f.output != "table" && f.output != "json" && f.output != "csv" {
		return fmt.Errorf("--output must be 'table', 'json' or 'csv', got %q", f.output)
	}
	if f.failOn != "" && f.failOn != "orphans" {
		return fmt.Errorf("--fail-on only supports 'orphans', got %q", f.failOn)
	}
	count := finder.PodCount(f.countPods)
	if count != finder.CountRunning && count != finder.CountAll {
		return fmt.Errorf("--count-pods must be 'running' or 'all', got %q", f.countPods)
	}
	minAge, err := finder.ParseAge(f.minAge)
	if err != nil {
		return fmt.Errorf("--min-age: %w", err)
	}
	prices, priced, err := loadPrices(f)
	if err != nil {
		return err
	}

	stderr := cmd.ErrOrStderr()
	warn := func(msg string) { fmt.Fprintln(stderr, "warning:", msg) }

	inv, err := load(cmd.Context(), f, warn)
	if err != nil {
		return err
	}

	res := finder.Classify(inv, finder.Options{CountPods: count, MinAge: minAge, Now: time.Now()})
	if priced {
		pricing.Apply(&res, prices)
		for _, sc := range res.UnpricedStorageClasses {
			warn(fmt.Sprintf("no price for storage class %q; set it with --price-file, --price %s=PRICE or --default-price", sc, sc))
		}
	}

	out := cmd.OutOrStdout()
	switch f.output {
	case "json":
		err = report.JSON(out, res)
	case "csv":
		err = report.CSV(out, res)
	default:
		err = report.Table(out, res, time.Now())
	}
	if err != nil {
		return err
	}

	if f.failOn == "orphans" && len(res.Orphans) > 0 {
		return &exitError{code: 1}
	}
	return nil
}

// loadPrices builds the price table from --price-file, --price,
// --default-price and --currency. priced is false when none was given.
func loadPrices(f scanFlags) (prices pricing.Prices, priced bool, err error) {
	if f.priceFile != "" {
		if prices, err = pricing.LoadFile(f.priceFile); err != nil {
			return prices, false, err
		}
		priced = true
	}
	for _, spec := range f.prices {
		if err := prices.SetClass(spec); err != nil {
			return prices, false, err
		}
		priced = true
	}
	if f.defaultPrice != "" {
		v, err := pricing.ParsePrice(f.defaultPrice)
		if err != nil {
			return prices, false, fmt.Errorf("--default-price: %w", err)
		}
		prices.Default = &v
		priced = true
	}
	if f.currency != "" {
		prices.Currency = f.currency
	}
	return prices, priced, nil
}

// load returns the inventory either from an offline dump or a live cluster.
func load(ctx context.Context, f scanFlags, warn func(string)) (finder.Inventory, error) {
	if f.input != "" {
		data, err := os.ReadFile(f.input)
		if err != nil {
			return finder.Inventory{}, fmt.Errorf("read input: %w", err)
		}
		inv, err := finder.ParseDump(data)
		if err != nil {
			return finder.Inventory{}, err
		}
		if f.allNamespaces {
			return inv, nil
		}
		return inv.FilterNamespaces(f.namespaces), nil
	}

	cs, contextNS, err := kube.NewClientset(f.kubeconfig)
	if err != nil {
		return finder.Inventory{}, err
	}
	namespaces := f.namespaces
	switch {
	case f.allNamespaces:
		namespaces = []string{""}
	case len(namespaces) == 0:
		namespaces = []string{contextNS}
	}
	return kube.Fetch(ctx, cs, namespaces, warn)
}

// exitError carries a specific process exit code up to main without printing a
// noisy "error:" line — used for the --fail-on CI gate.
type exitError struct{ code int }

func (e *exitError) Error() string { return fmt.Sprintf("exit %d", e.code) }
