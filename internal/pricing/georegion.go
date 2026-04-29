// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package pricing

import (
	"math"
	"sort"
)

// georegion.go — nearest-region ranking for each registered cloud
// provider. Pairs with country_geo.go (country centroids) to resolve
// --data-center-location <CC> into the closest provider region slug.
//
// Used by:
//   - xapiri dashboard: rank 4 nearest regions for cost-compare display
//   - cost.WarmCaches: pre-heat pricing caches for nearby regions
//   - xapiri step-6 prompts: geo-bracket defaults
//
// Centroid coordinates are the lat/lon of the data-center location
// for each provider region slug. They do not need to be exact —
// great-circle distance at this scale only needs city-level accuracy.

type regionPoint struct {
	id       string
	lat, lon float64
}

func regionCentroids(prov string) []regionPoint {
	switch prov {
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
	}
	return nil
}

func haversineKm(lat1, lon1, lat2, lon2 float64) float64 {
	const rEarth = 6371.0
	p1 := lat1 * math.Pi / 180
	p2 := lat2 * math.Pi / 180
	dlat := (lat2 - lat1) * math.Pi / 180
	dlon := (lon2 - lon1) * math.Pi / 180
	a := math.Sin(dlat/2)*math.Sin(dlat/2) + math.Cos(p1)*math.Cos(p2)*math.Sin(dlon/2)*math.Sin(dlon/2)
	return rEarth * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

// GeoRankedRegions returns the nearest region slugs for the named provider,
// sorted by great-circle distance from (lat, lon), capped at limit.
// Returns nil when provider has no centroid table.
func GeoRankedRegions(prov string, lat, lon float64, limit int) []string {
	pts := regionCentroids(prov)
	if len(pts) == 0 || limit <= 0 {
		return nil
	}
	type scored struct {
		id string
		km float64
	}
	xs := make([]scored, 0, len(pts))
	for _, p := range pts {
		xs = append(xs, scored{id: p.id, km: haversineKm(lat, lon, p.lat, p.lon)})
	}
	sort.Slice(xs, func(i, j int) bool { return xs[i].km < xs[j].km })
	seen := map[string]struct{}{}
	out := make([]string, 0, limit)
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

// GeoNearestRegion returns the single closest region slug for provider.
// Returns "" when the provider has no centroid table.
func GeoNearestRegion(prov string, lat, lon float64) string {
	r := GeoRankedRegions(prov, lat, lon, 1)
	if len(r) == 0 {
		return ""
	}
	return r[0]
}

// GeoHasCentroids reports whether provider has a centroid table for
// geo-based region selection.
func GeoHasCentroids(prov string) bool {
	return len(regionCentroids(prov)) > 0
}
