// Package report renders a finder.Result as a human table or stable JSON.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/moveeeax/k8s-orphaned-pvc-finder/internal/finder"
)

// gib is one gibibyte in bytes.
const gib = 1024 * 1024 * 1024

// GiB renders a byte count as a gibibyte figure with two decimals.
func GiB(bytes int64) string {
	return fmt.Sprintf("%.2f GiB", float64(bytes)/float64(gib))
}

// age turns an RFC3339 creation timestamp into a compact human age relative to
// now. An empty or unparseable timestamp renders as "-".
func age(creation string, now time.Time) string {
	if creation == "" {
		return "-"
	}
	t, err := time.Parse(time.RFC3339, creation)
	if err != nil {
		return "-"
	}
	d := now.Sub(t)
	if d < 0 {
		d = 0
	}
	days := int(d.Hours()) / 24
	if days > 0 {
		return fmt.Sprintf("%dd", days)
	}
	hours := int(d.Hours())
	if hours > 0 {
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}

// Table writes a padded, human-readable report to w. now anchors the age
// column (injected so tests are deterministic).
func Table(w io.Writer, res finder.Result, now time.Time) error {
	if len(res.Orphans) == 0 {
		_, err := fmt.Fprintln(w, "No orphaned PersistentVolumeClaims found. 🎉")
		return err
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAMESPACE\tNAME\tPHASE\tSTORAGECLASS\tCAPACITY\tAGE")
	for _, o := range res.Orphans {
		sc := o.StorageClass
		if sc == "" {
			sc = "<none>"
		}
		cap := o.Capacity
		if cap == "" {
			cap = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			o.Namespace, o.Name, o.Phase, sc, cap, age(o.CreationTimestamp, now))
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	fmt.Fprintf(w, "\n%d orphaned PVC(s), %s wasted total\n", len(res.Orphans), GiB(res.TotalBytes))

	classes := make([]string, 0, len(res.ByStorageClass))
	for sc := range res.ByStorageClass {
		classes = append(classes, sc)
	}
	sort.Strings(classes)
	for _, sc := range classes {
		fmt.Fprintf(w, "  %-20s %s\n", sc, GiB(res.ByStorageClass[sc]))
	}
	return nil
}

// JSON writes the result as indented, stable JSON. Orphans are pre-sorted by
// Classify; map keys are emitted deterministically by encoding/json.
func JSON(w io.Writer, res finder.Result) error {
	if res.Orphans == nil {
		res.Orphans = []finder.Orphan{}
	}
	if res.ByStorageClass == nil {
		res.ByStorageClass = map[string]int64{}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(res)
}
