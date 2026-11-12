// Package report renders a finder.Result as a human table or stable JSON.
package report

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
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

// accessModeShort maps access modes to the abbreviations kubectl prints.
var accessModeShort = map[string]string{
	"ReadWriteOnce":    "RWO",
	"ReadOnlyMany":     "ROX",
	"ReadWriteMany":    "RWX",
	"ReadWriteOncePod": "RWOP",
}

// accessModes renders access modes the way `kubectl get pvc` does.
func accessModes(modes []string) string {
	if len(modes) == 0 {
		return "-"
	}
	out := make([]string, len(modes))
	for i, m := range modes {
		if short, ok := accessModeShort[m]; ok {
			out[i] = short
		} else {
			out[i] = m
		}
	}
	return strings.Join(out, ",")
}

// money renders a cost with two decimals and the currency, if any.
func money(v float64, currency string) string {
	if currency == "" {
		return fmt.Sprintf("%.2f", v)
	}
	return fmt.Sprintf("%.2f %s", v, currency)
}

// reason renders the REASON column, naming the finished Pod when there is one.
func reason(o finder.Orphan) string {
	if o.LastUsedBy != "" {
		return fmt.Sprintf("%s (%s)", o.Reason, o.LastUsedBy)
	}
	return o.Reason
}

// Table writes a padded, human-readable report to w. now anchors the age
// column (injected so tests are deterministic). A COST/MO column and cost
// totals appear only when pricing.Apply has run on res; an unpriced claim
// shows "?".
func Table(w io.Writer, res finder.Result, now time.Time) error {
	priced := res.TotalMonthlyCost != nil
	if len(res.Orphans) == 0 {
		if _, err := fmt.Fprintln(w, "No orphaned PersistentVolumeClaims found. 🎉"); err != nil {
			return err
		}
		return writeRetained(w, res)
	}

	unpriced := map[string]bool{}
	for _, sc := range res.UnpricedStorageClasses {
		unpriced[sc] = true
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	header := "NAMESPACE\tNAME\tPHASE\tSTORAGECLASS\tACCESS\tCAPACITY\tAGE\tREASON"
	if priced {
		header += "\tCOST/MO"
	}
	fmt.Fprintln(tw, header)
	for _, o := range res.Orphans {
		sc := o.StorageClass
		if sc == "" {
			sc = finder.NoStorageClass
		}
		cap := o.Capacity
		if cap == "" {
			cap = "-"
		}
		row := fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s",
			o.Namespace, o.Name, o.Phase, sc, accessModes(o.AccessModes), cap, age(o.CreationTimestamp, now), reason(o))
		if priced {
			cost := "?"
			if o.MonthlyCost != nil {
				cost = money(*o.MonthlyCost, res.Currency)
			}
			row += "\t" + cost
		}
		fmt.Fprintln(tw, row)
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	summary := fmt.Sprintf("\n%d orphaned PVC(s), %s wasted total", len(res.Orphans), GiB(res.TotalBytes))
	if priced {
		summary += fmt.Sprintf(", %s/month", money(*res.TotalMonthlyCost, res.Currency))
		if len(res.UnpricedStorageClasses) > 0 {
			summary += fmt.Sprintf(" (excludes unpriced: %s)", strings.Join(res.UnpricedStorageClasses, ", "))
		}
	}
	fmt.Fprintln(w, summary)

	classes := make([]string, 0, len(res.ByStorageClass))
	for sc := range res.ByStorageClass {
		classes = append(classes, sc)
	}
	sort.Strings(classes)
	for _, sc := range classes {
		line := fmt.Sprintf("  %-20s %-12s", sc, GiB(res.ByStorageClass[sc]))
		if priced {
			cost := "?"
			if !unpriced[sc] {
				cost = money(res.MonthlyCostByStorageClass[sc], res.Currency) + "/month"
			}
			line += " " + cost
		}
		fmt.Fprintln(w, strings.TrimRight(line, " "))
	}
	return writeRetained(w, res)
}

// writeRetained notes claims skipped because a live StatefulSet owns them.
func writeRetained(w io.Writer, res finder.Result) error {
	if res.StatefulSetRetained == 0 {
		return nil
	}
	_, err := fmt.Fprintf(w, "\n%d unreferenced PVC(s) skipped: owned by a live StatefulSet volumeClaimTemplate\n", res.StatefulSetRetained)
	return err
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

// csvHeader is the fixed CSV column order. Cost columns are always present and
// left empty when no price applies, so the schema does not depend on flags.
var csvHeader = []string{
	"namespace", "name", "phase", "storageClass", "accessModes", "capacity",
	"capacityBytes", "creationTimestamp", "reason", "lastUsedBy", "monthlyCost", "currency",
}

// CSV writes one row per orphan, for spreadsheets and scheduled reports.
// accessModes are joined with ";". Totals are left to the consumer.
func CSV(w io.Writer, res finder.Result) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(csvHeader); err != nil {
		return err
	}
	for _, o := range res.Orphans {
		cost, currency := "", ""
		if o.MonthlyCost != nil {
			cost = strconv.FormatFloat(*o.MonthlyCost, 'f', 2, 64)
			currency = res.Currency
		}
		if err := cw.Write([]string{
			o.Namespace, o.Name, o.Phase, o.StorageClass, strings.Join(o.AccessModes, ";"), o.Capacity,
			strconv.FormatInt(o.CapacityBytes, 10), o.CreationTimestamp, o.Reason, o.LastUsedBy, cost, currency,
		}); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}
