// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package xapiri

// georegions.go — outbound-IP geolocation + nearest region hints for
// xapiri. For every registered provider that has a centroid table,
// empty Region / Location string fields are filled from great-circle
// distance to the operator's outbound IP (GeoJS) before cloud cost
// compare, and the same hints pre-fill step-6 prompts. Providers
// without a table (e.g. Proxmox arbitrary strings) are skipped.

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/pricing"
	"github.com/lpasquali/yage/internal/provider"
)

const geojsURL = "https://get.geojs.io/v1/ip/geo.json"

type regionPoint struct {
	id string
	lat, lon float64
}

// fetchOutboundIPGeo returns approximate lat/lon from the public
// GeoJS endpoint (no API key). Fails soft on timeout / airgap.
func fetchOutboundIPGeo(c *http.Client) (lat, lon float64, label string, err error) {
	if c == nil {
		c = &http.Client{Timeout: 5 * time.Second}
	}
	req, err := http.NewRequest(http.MethodGet, geojsURL, nil)
	if err != nil {
		return 0, 0, "", err
	}
	req.Header.Set("User-Agent", "yage/xapiri (+https://github.com/lpasquali/yage)")
	resp, err := c.Do(req)
	if err != nil {
		return 0, 0, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, 0, "", fmt.Errorf("geo http %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, 0, "", err
	}
	return parseGeoJSBody(body)
}

func readGeoFixtureFile(path string) (lat, lon float64, label string, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, "", err
	}
	return parseGeoJSBody(b)
}

func parseGeoJSBody(body []byte) (lat, lon float64, label string, err error) {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return 0, 0, "", err
	}
	lat, err = coerceFloat(m["latitude"])
	if err != nil {
		return 0, 0, "", fmt.Errorf("geo: latitude: %w", err)
	}
	lon, err = coerceFloat(m["longitude"])
	if err != nil {
		return 0, 0, "", fmt.Errorf("geo: longitude: %w", err)
	}
	city, _ := m["city"].(string)
	cc, _ := m["country_code"].(string)
	if strings.TrimSpace(city) != "" && strings.TrimSpace(cc) != "" {
		label = fmt.Sprintf("%s, %s", strings.TrimSpace(city), strings.TrimSpace(cc))
	} else if strings.TrimSpace(cc) != "" {
		label = strings.TrimSpace(cc)
	}
	return lat, lon, label, nil
}

func coerceFloat(v any) (float64, error) {
	switch x := v.(type) {
	case float64:
		return x, nil
	case string:
		return strconv.ParseFloat(strings.TrimSpace(x), 64)
	default:
		return 0, fmt.Errorf("unexpected type %T", v)
	}
}

func haversineKm(lat1, lon1, lat2, lon2 float64) float64 {
	const rEarth = 6371.0
	p1 := lat1 * math.Pi / 180
	p2 := lat2 * math.Pi / 180
	dlat := (lat2 - lat1) * math.Pi / 180
	dlon := (lon2 - lon1) * math.Pi / 180
	a := math.Sin(dlat/2)*math.Sin(dlat/2) + math.Cos(p1)*math.Cos(p2)*math.Sin(dlon/2)*math.Sin(dlon/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return rEarth * c
}

func regionCentroids(provider string) []regionPoint {
	switch provider {
	case "aws":
		return []regionPoint{
			{"us-east-1", 39.04, -77.49},
			{"us-east-2", 39.96, -83.00},
			{"us-west-1", 37.35, -121.96},
			{"us-west-2", 45.51, -122.68},
			{"ca-central-1", 45.50, -73.57},
			{"eu-west-1", 53.35, -6.26},
			{"eu-west-2", 51.51, -0.13},
			{"eu-west-3", 48.86, 2.35},
			{"eu-central-1", 50.11, 8.68},
			{"eu-north-1", 59.33, 18.07},
			{"ap-southeast-1", 1.35, 103.82},
			{"ap-southeast-2", -33.87, 151.21},
			{"ap-northeast-1", 35.68, 139.65},
			{"ap-northeast-2", 37.57, 126.98},
			{"ap-south-1", 19.08, 72.88},
			{"sa-east-1", -23.55, -46.63},
			{"me-south-1", 26.07, 50.56},
			{"af-south-1", -33.92, 18.42},
		}
	case "azure":
		return []regionPoint{
			{"eastus", 37.37, -79.82},
			{"eastus2", 36.67, -78.80},
			{"westus2", 47.23, -119.85},
			{"westus3", 33.45, -112.07},
			{"centralus", 41.88, -87.63},
			{"northeurope", 53.35, -6.26},
			{"westeurope", 52.37, 4.89},
			{"uksouth", 51.51, -0.13},
			{"francecentral", 48.86, 2.35},
			{"germanywestcentral", 50.11, 8.68},
			{"switzerlandnorth", 47.38, 8.54},
			{"norwayeast", 59.91, 10.75},
			{"swedencentral", 59.33, 18.07},
			{"polandcentral", 52.23, 21.01},
			{"italynorth", 45.46, 9.19},
			{"australiaeast", -33.87, 151.21},
			{"southeastasia", 1.35, 103.82},
			{"japaneast", 35.68, 139.65},
			{"japanwest", 34.69, 135.50},
			{"koreacentral", 37.57, 126.98},
			{"brazilsouth", -23.55, -46.63},
			{"southafricanorth", -25.75, 28.23},
		}
	case "gcp":
		return []regionPoint{
			{"us-central1", 41.26, -95.86},
			{"us-east1", 33.84, -81.16},
			{"us-east4", 39.04, -77.49},
			{"us-west1", 45.51, -122.68},
			{"us-west2", 34.05, -118.24},
			{"europe-west1", 50.45, 3.98},
			{"europe-west2", 51.51, -0.13},
			{"europe-west3", 50.11, 8.68},
			{"europe-west4", 53.44, 6.84},
			{"europe-west6", 47.38, 8.54},
			{"europe-west8", 41.90, 12.50},
			{"europe-west9", 48.86, 2.35},
			{"europe-north1", 60.17, 24.94},
			{"asia-east1", 25.03, 121.57},
			{"asia-northeast1", 35.68, 139.65},
			{"asia-southeast1", 1.35, 103.82},
			{"asia-south1", 19.08, 72.88},
			{"australia-southeast1", -37.81, 144.96},
		}
	case "hetzner":
		return []regionPoint{
			{"fsn1", 50.48, 12.36},
			{"nbg1", 49.45, 11.08},
			{"hel1", 60.17, 24.94},
			{"ash", 39.04, -77.49},
			{"hil", 45.52, -122.99},
			{"sin", 1.35, 103.82},
		}
	case "digitalocean":
		return []regionPoint{
			{"nyc3", 40.71, -74.01},
			{"nyc1", 40.71, -74.01},
			{"sfo3", 37.77, -122.42},
			{"ams3", 52.37, 4.90},
			{"fra1", 50.11, 8.68},
			{"lon1", 51.51, -0.13},
			{"sgp1", 1.35, 103.82},
			{"tor1", 43.65, -79.38},
			{"blr1", 12.97, 77.59},
			{"syd1", -33.87, 151.21},
		}
	case "linode":
		return []regionPoint{
			{"us-east", 40.74, -74.17},
			{"us-central", 32.78, -96.80},
			{"us-west", 37.55, -121.99},
			{"us-southeast", 33.75, -84.39},
			{"us-iowa", 43.15, -93.20},
			{"ca-central", 43.65, -79.38},
			{"eu-west", 51.51, -0.13},
			{"eu-central", 50.11, 8.68},
			{"ap-west", 1.35, 103.82},
			{"ap-south", 19.08, 72.88},
			{"ap-northeast", 35.68, 139.65},
			{"ap-southeast", -33.87, 151.21},
		}
	case "oci":
		return []regionPoint{
			{"us-ashburn-1", 39.04, -77.49},
			{"us-phoenix-1", 33.45, -112.07},
			{"eu-frankfurt-1", 50.11, 8.68},
			{"uk-london-1", 51.51, -0.13},
			{"eu-amsterdam-1", 52.37, 4.90},
			{"ap-sydney-1", -33.87, 151.21},
			{"ap-mumbai-1", 19.08, 72.88},
			{"ap-osaka-1", 34.69, 135.50},
			{"ca-toronto-1", 43.65, -79.38},
			{"sa-saopaulo-1", -23.55, -46.63},
		}
	case "ibmcloud":
		return []regionPoint{
			{"us-south", 32.78, -96.80},
			{"eu-gb", 51.51, -0.13},
			{"eu-de", 50.11, 8.68},
			{"jp-tok", 35.68, 139.65},
			{"au-syd", -33.87, 151.21},
			{"ca-tor", 45.50, -73.57},
		}
	default:
		return nil
	}
}

// geoNearestRegionID returns the closest region / location slug for
// provider to (lat, lon). Empty when provider has no table.
func geoNearestRegionID(provider string, lat, lon float64) string {
	best := geoRankedRegions(provider, lat, lon, 1)
	if len(best) == 0 {
		return ""
	}
	return best[0]
}

// geoRankedRegions returns the nearest region IDs for provider,
// sorted by great-circle distance, capped at limit.
func geoRankedRegions(provider string, lat, lon float64, limit int) []string {
	pts := regionCentroids(provider)
	if len(pts) == 0 || limit <= 0 {
		return nil
	}
	type scored struct {
		id  string
		km  float64
	}
	var xs []scored
	for _, p := range pts {
		xs = append(xs, scored{id: p.id, km: haversineKm(lat, lon, p.lat, p.lon)})
	}
	sort.Slice(xs, func(i, j int) bool { return xs[i].km < xs[j].km })
	seen := map[string]struct{}{}
	var out []string
	for _, x := range xs {
		if _, ok := seen[x.id]; ok {
			continue
		}
		seen[x.id] = struct{}{}
		out = append(out, x.id)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// geoHasCentroids reports whether we can suggest public regions for
// this provider registry id (e.g. "aws", "ibmcloud").
func geoHasCentroids(provider string) bool {
	return len(regionCentroids(provider)) > 0
}

// geoRegionLikeFieldNames are cfg.Providers.* string fields we treat
// as geography selectors for live pricing / manifests.
var geoRegionLikeFieldNames = []string{"Region", "Location"}

// ApplyDataCenterLocationDefaults fills empty Region / Location
// fields on every registered provider sub-struct using the country
// centroid named by cfg.Cost.Currency.DataCenterLocation
// (--data-center-location). Returns the same kind of human-readable
// lines applyGeoRegionDefaults emits, plus a leading source label
// so callers can log "from --data-center-location IT".
//
// No-op when the location is empty or unknown to pricing.CountryCentroid.
// This is the non-TUI counterpart to xapiri.stampGeoRegions; main()
// can call it right after config.Load to make --data-center-location
// drive provider regions even on non-interactive runs.
func ApplyDataCenterLocationDefaults(cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	dc := strings.ToUpper(strings.TrimSpace(cfg.Cost.Currency.DataCenterLocation))
	if dc == "" {
		return nil
	}
	lat, lon, ok := pricing.CountryCentroid(dc)
	if !ok {
		return []string{fmt.Sprintf(
			"--data-center-location %s: no centroid in our country table; skipping region fill", dc)}
	}
	return applyGeoRegionDefaults(cfg, lat, lon)
}

// applyGeoRegionDefaults sets empty Region / Location string fields
// on every registered provider sub-struct that has a centroid table.
// The returned lines are human-readable "provider.Field=value" rows
// for xapiri visibility (empty when nothing changed).
func applyGeoRegionDefaults(cfg *config.Config, lat, lon float64) []string {
	if cfg == nil {
		return nil
	}
	var lines []string
	for _, p := range provider.Registered() {
		if !geoHasCentroids(p) {
			continue
		}
		g := geoNearestRegionID(p, lat, lon)
		if g == "" {
			continue
		}
		sub, ok := providerSubStruct(cfg, p)
		if !ok {
			continue
		}
		for _, fname := range geoRegionLikeFieldNames {
			fv := sub.FieldByName(fname)
			if !fv.IsValid() || fv.Kind() != reflect.String || !fv.CanSet() {
				continue
			}
			if strings.TrimSpace(fv.String()) != "" {
				continue
			}
			fv.SetString(g)
			lines = append(lines, fmt.Sprintf("%s.%s=%s (nearest to outbound IP)", p, fname, g))
			break
		}
	}
	return lines
}

// geoBracketDefault returns a bracket default for a prompted provider
// string field when geo lookup succeeded.
func geoBracketDefault(provider, fieldName string, lat, lon float64, geoOK bool) string {
	if !geoOK || !geoHasCentroids(provider) {
		return ""
	}
	for _, fname := range geoRegionLikeFieldNames {
		if fname == fieldName {
			return geoNearestRegionID(provider, lat, lon)
		}
	}
	return ""
}

// stampGeoRegions resolves the outbound IP once, stamps cfg, and prints
// a single user-visible line. context completes "geo: <label> — …".
func (s *state) stampGeoRegions(contextLine string) {
	s.ensureGeoLookup()
	if !s.geoOK {
		return
	}
	fills := applyGeoRegionDefaults(s.cfg, s.geoLat, s.geoLon)
	if s.geoLabel != "" {
		s.r.info("geo: %s — %s (YAGE_XAPIRI_NO_GEO=1 to skip).", s.geoLabel, contextLine)
	} else {
		s.r.info("geo: outbound IP resolved — %s (YAGE_XAPIRI_NO_GEO=1 to skip).", contextLine)
	}
	if len(fills) > 0 {
		s.r.info("  geo fills applied:")
		for _, ln := range fills {
			s.r.info("    %s", ln)
		}
	} else {
		s.r.info("  geo fills: none (regions already set, or no centroid table for this provider set).")
	}
}

func (s *state) ensureGeoLookup() {
	if s == nil || s.cfg == nil {
		return
	}
	if s.geoDidLookup {
		return
	}
	s.geoDidLookup = true
	if s.cfg.Airgapped || strings.TrimSpace(os.Getenv("YAGE_XAPIRI_NO_GEO")) == "1" {
		return
	}
	var lat, lon float64
	var label string
	var err error
	if p := strings.TrimSpace(os.Getenv("YAGE_XAPIRI_GEO_FIXTURE")); p != "" {
		lat, lon, label, err = readGeoFixtureFile(p)
	} else {
		c := &http.Client{Timeout: 5 * time.Second}
		lat, lon, label, err = fetchOutboundIPGeo(c)
	}
	if err != nil {
		s.geoOK = false
		return
	}
	s.geoLat, s.geoLon, s.geoLabel = lat, lon, label
	s.geoOK = true
}
