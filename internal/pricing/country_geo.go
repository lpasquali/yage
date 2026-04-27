// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package pricing

import "strings"

// country_geo.go — coarse country-code → lat/lon centroid for the
// ISO-3166 alpha-2 codes we recognize in countryToCurrency. Used by
// the --data-center-location flag to pick the nearest provider
// region (haversine vs centroid tables in xapiri/georegions.go).
//
// Centroids are capital-city coordinates; that's accurate enough for
// "which AWS region is closest" because cloud regions cluster near
// the same major cities anyway. Resolution is whole-degree-ish.
//
// Keep this in lockstep with countryToCurrency: every country code
// surfaced via that map should have a centroid here so the flag can
// always resolve. The reverse isn't required (extra centroids are
// harmless), but missing entries silently degrade --data-center-
// location to "currency only, no region fill".

type countryPoint struct {
	lat, lon float64
}

var countryCentroids = map[string]countryPoint{
	// Americas
	"US": {38.90, -77.04},   // Washington
	"CA": {45.42, -75.69},   // Ottawa
	"MX": {19.43, -99.13},   // Mexico City
	"BR": {-15.78, -47.92},  // Brasília
	"AR": {-34.61, -58.38},  // Buenos Aires
	"CL": {-33.45, -70.66},  // Santiago
	"CO": {4.71, -74.07},    // Bogotá
	"PE": {-12.05, -77.04},  // Lima
	"VE": {10.49, -66.88},   // Caracas
	"UY": {-34.90, -56.16},  // Montevideo

	// Europe (Eurozone)
	"IE": {53.35, -6.26}, "FR": {48.86, 2.35}, "DE": {52.52, 13.40},
	"IT": {41.90, 12.50}, "ES": {40.42, -3.70}, "PT": {38.72, -9.14},
	"NL": {52.37, 4.89}, "BE": {50.85, 4.35}, "AT": {48.21, 16.37},
	"FI": {60.17, 24.94}, "LU": {49.61, 6.13}, "GR": {37.98, 23.73},
	"MT": {35.90, 14.51}, "CY": {35.17, 33.36}, "EE": {59.44, 24.75},
	"LV": {56.95, 24.11}, "LT": {54.69, 25.28}, "SI": {46.06, 14.51},
	"SK": {48.15, 17.11}, "HR": {45.81, 15.98},

	// Europe (non-Eurozone)
	"GB": {51.51, -0.13}, "CH": {46.95, 7.45},
	"SE": {59.33, 18.07}, "NO": {59.91, 10.75}, "DK": {55.68, 12.57},
	"IS": {64.13, -21.82}, "PL": {52.23, 21.01}, "CZ": {50.08, 14.44},
	"HU": {47.50, 19.04}, "RO": {44.43, 26.10}, "BG": {42.70, 23.32},
	"RU": {55.75, 37.62}, "TR": {39.93, 32.86}, "UA": {50.45, 30.52},
	"KZ": {51.17, 71.43},

	// Asia
	"JP": {35.68, 139.65}, "CN": {39.90, 116.40}, "HK": {22.32, 114.17},
	"TW": {25.03, 121.57}, "KR": {37.57, 126.98}, "IN": {28.61, 77.21},
	"PK": {33.69, 73.05}, "BD": {23.81, 90.41}, "LK": {6.93, 79.86},
	"NP": {27.72, 85.32}, "SG": {1.35, 103.82}, "MY": {3.14, 101.69},
	"TH": {13.75, 100.50}, "VN": {21.03, 105.85}, "ID": {-6.21, 106.85},
	"PH": {14.60, 120.98},

	// Oceania
	"AU": {-35.28, 149.13}, "NZ": {-41.29, 174.78},

	// Middle East / North Africa
	"AE": {24.47, 54.37}, "SA": {24.71, 46.68}, "IL": {31.78, 35.22},
	"EG": {30.05, 31.25}, "QA": {25.29, 51.53}, "KW": {29.38, 47.99},
	"OM": {23.59, 58.41}, "BH": {26.23, 50.59}, "JO": {31.95, 35.93},

	// Sub-Saharan Africa
	"ZA": {-25.75, 28.23}, "NG": {9.08, 7.40}, "KE": {-1.29, 36.82},
	"GH": {5.60, -0.19}, "MA": {34.02, -6.83}, "TN": {36.81, 10.18},
	"DZ": {36.75, 3.06},
}

// CountryCentroid returns the (lat, lon) centroid for a given ISO-
// 3166 alpha-2 country code. ok is false when the code is unknown.
// Casing is normalized to uppercase before lookup.
func CountryCentroid(code string) (lat, lon float64, ok bool) {
	c := strings.ToUpper(strings.TrimSpace(code))
	if c == "" {
		return 0, 0, false
	}
	p, ok := countryCentroids[c]
	if !ok {
		return 0, 0, false
	}
	return p.lat, p.lon, true
}

// CountryCurrency returns the ISO-4217 currency code for a country.
// Empty when the country isn't mapped — callers should fall back to
// USD and (optionally) print UnsupportedCurrencyMessage.
func CountryCurrency(code string) string {
	c := strings.ToUpper(strings.TrimSpace(code))
	if c == "" {
		return ""
	}
	return countryToCurrency[c]
}
