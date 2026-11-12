// Package pricing turns orphaned capacity into an estimated monthly cost from
// a user-supplied price table. There is no built-in price list: storage
// prices differ by provider and region and change over time.
package pricing

import (
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/moveeeax/k8s-orphaned-pvc-finder/internal/finder"
)

// gib is one gibibyte in bytes.
const gib = 1024 * 1024 * 1024

// Prices maps storage classes to a price per GiB-month. The class key for a
// claim with no storage class is "<none>". Default, when set, prices every
// class not listed in Classes.
//
// File form (YAML or JSON):
//
//	currency: USD
//	default: 0.10
//	classes:
//	  gp3: 0.08
//	  io2: 0.125
//	  "<none>": 0.10
type Prices struct {
	Currency string             `json:"currency"`
	Default  *float64           `json:"default,omitempty"`
	Classes  map[string]float64 `json:"classes"`
}

// LoadFile reads a price file. Unknown keys are rejected so a typo such as
// "clases" fails loudly instead of pricing nothing.
func LoadFile(path string) (Prices, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Prices{}, fmt.Errorf("read price file: %w", err)
	}
	var p Prices
	if err := yaml.UnmarshalStrict(data, &p); err != nil {
		return Prices{}, fmt.Errorf("parse price file %s: %w", path, err)
	}
	if err := p.validate(); err != nil {
		return Prices{}, fmt.Errorf("price file %s: %w", path, err)
	}
	return p, nil
}

func (p Prices) validate() error {
	if p.Default != nil && !validPrice(*p.Default) {
		return fmt.Errorf("default price must be a finite number >= 0, got %v", *p.Default)
	}
	for class, v := range p.Classes {
		if !validPrice(v) {
			return fmt.Errorf("price for class %q must be a finite number >= 0, got %v", class, v)
		}
	}
	return nil
}

func validPrice(v float64) bool { return v >= 0 && !math.IsInf(v, 0) && !math.IsNaN(v) }

// SetClass parses a CLASS=PRICE override (the --price flag) into p.
func (p *Prices) SetClass(spec string) error {
	class, raw, ok := strings.Cut(spec, "=")
	if !ok || class == "" {
		return fmt.Errorf("--price wants CLASS=PRICE, got %q", spec)
	}
	v, err := ParsePrice(raw)
	if err != nil {
		return fmt.Errorf("--price %s: %w", spec, err)
	}
	if p.Classes == nil {
		p.Classes = map[string]float64{}
	}
	p.Classes[class] = v
	return nil
}

// ParsePrice parses one per-GiB-month price.
func ParsePrice(s string) (float64, error) {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || !validPrice(v) {
		return 0, fmt.Errorf("price must be a number >= 0, got %q", s)
	}
	return v, nil
}

// Lookup returns the price per GiB-month for a storage class ("" means no
// class), falling back to Default.
func (p Prices) Lookup(class string) (float64, bool) {
	if class == "" {
		class = finder.NoStorageClass
	}
	if v, ok := p.Classes[class]; ok {
		return v, true
	}
	if p.Default != nil {
		return *p.Default, true
	}
	return 0, false
}

// round2 rounds to cents.
func round2(v float64) float64 { return math.Round(v*100) / 100 }

// Apply fills the cost fields of res. An orphan in a class with no price gets
// no MonthlyCost, its class goes into UnpricedStorageClasses and it is left out
// of the totals: an unknown price is never counted as zero.
func Apply(res *finder.Result, p Prices) {
	res.Currency = p.Currency
	res.MonthlyCostByStorageClass = map[string]float64{}
	unpriced := map[string]bool{}
	var total float64
	for i := range res.Orphans {
		o := &res.Orphans[i]
		class := o.StorageClass
		if class == "" {
			class = finder.NoStorageClass
		}
		price, ok := p.Lookup(class)
		if !ok {
			unpriced[class] = true
			continue
		}
		cost := float64(o.CapacityBytes) / gib * price
		c := round2(cost)
		o.MonthlyCost = &c
		res.MonthlyCostByStorageClass[class] += cost
		total += cost
	}
	for class, v := range res.MonthlyCostByStorageClass {
		res.MonthlyCostByStorageClass[class] = round2(v)
	}
	t := round2(total)
	res.TotalMonthlyCost = &t
	res.UnpricedStorageClasses = nil
	for class := range unpriced {
		res.UnpricedStorageClasses = append(res.UnpricedStorageClasses, class)
	}
	sort.Strings(res.UnpricedStorageClasses)
}
