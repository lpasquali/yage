package pricing

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Linode/Akamai catalog API — auth-free for type listing.
//   GET https://api.linode.com/v4/linode/types
// Returns every type with price.monthly + price.hourly (USD), plus
// the per-region price overrides (region_prices[]). When the region
// has an override we honor it; otherwise the global price applies.
const linodeTypesURL = "https://api.linode.com/v4/linode/types?page_size=200"

type linodeFetcher struct{ httpClient *http.Client }

func init() {
	Register("linode", &linodeFetcher{httpClient: &http.Client{Timeout: 15 * time.Second}})
}

type linodePrice struct {
	Hourly  float64 `json:"hourly"`
	Monthly float64 `json:"monthly"`
}

type linodeRegionPrice struct {
	ID      string      `json:"id"`
	Hourly  float64     `json:"hourly"`
	Monthly float64     `json:"monthly"`
}

type linodeType struct {
	ID            string              `json:"id"`
	Label         string              `json:"label"`
	Price         linodePrice         `json:"price"`
	RegionPrices  []linodeRegionPrice `json:"region_prices"`
}

type linodeTypesResp struct {
	Data []linodeType `json:"data"`
}

func (l *linodeFetcher) Fetch(typeID, region string) (Item, error) {
	req, _ := http.NewRequest("GET", linodeTypesURL, nil)
	req.Header.Set("User-Agent", "bootstrap-capi/pricing")
	resp, err := l.httpClient.Do(req)
	if err != nil {
		return Item{}, fmt.Errorf("linode: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return Item{}, fmt.Errorf("linode: HTTP %d", resp.StatusCode)
	}
	var lr linodeTypesResp
	if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil {
		return Item{}, fmt.Errorf("linode decode: %w", err)
	}
	for _, t := range lr.Data {
		if t.ID != typeID {
			continue
		}
		hourly := t.Price.Hourly
		monthly := t.Price.Monthly
		// Honor per-region price overrides when the type has them.
		for _, rp := range t.RegionPrices {
			if rp.ID == region {
				hourly = rp.Hourly
				monthly = rp.Monthly
				break
			}
		}
		return Item{
			USDPerHour:  hourly,
			USDPerMonth: monthly,
			FetchedAt:   time.Now(),
		}, nil
	}
	return Item{}, fmt.Errorf("linode: unknown type %q", typeID)
}
