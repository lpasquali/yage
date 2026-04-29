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
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/pricing"
	"github.com/lpasquali/yage/internal/provider"
)

const geojsURL = "https://get.geojs.io/v1/ip/geo.json"


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

// geoNearestRegionID returns the closest region slug for provider to
// (lat, lon). Empty when the provider has no centroid table.
func geoNearestRegionID(prov string, lat, lon float64) string {
	return pricing.GeoNearestRegion(prov, lat, lon)
}

// geoRankedRegions returns the nearest region IDs for provider,
// sorted by great-circle distance, capped at limit.
// Delegates to pricing.GeoRankedRegions so the centroid table lives
// in one place and is shared with cost.WarmCaches.
func geoRankedRegions(prov string, lat, lon float64, limit int) []string {
	return pricing.GeoRankedRegions(prov, lat, lon, limit)
}

// geoHasCentroids reports whether we can suggest public regions for
// this provider registry id (e.g. "aws", "ibmcloud").
func geoHasCentroids(prov string) bool {
	return pricing.GeoHasCentroids(prov)
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
	if s.cfg.Airgapped || !s.cfg.GeoIPEnabled || strings.TrimSpace(os.Getenv("YAGE_XAPIRI_NO_GEO")) == "1" {
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
