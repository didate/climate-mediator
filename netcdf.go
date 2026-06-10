package main

import (
	"fmt"
	"math"
	"os"

	"github.com/nilsmagnus/grib/griblib"
)

// parseNetCDF parses a GRIB file and extracts grid data for the given variable.
func parseNetCDF(path, variable string, year, month int) (*CDSGridData, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	messages, err := griblib.ReadMessages(f)
	if err != nil {
		return nil, fmt.Errorf("read grib: %w", err)
	}

	if len(messages) == 0 {
		return nil, fmt.Errorf("no messages in grib file")
	}

	// Use the first message
	msg := messages[0]

	data := msg.Section7.Data
	if len(data) == 0 {
		return nil, fmt.Errorf("empty data in grib message")
	}

	// Extract grid definition (Grid0 = lat/lon grid)
	gridDef, ok := msg.Section3.Definition.(*griblib.Grid0)
	if !ok {
		return nil, fmt.Errorf("unsupported grid type, expected Grid0 (lat/lon)")
	}

	nLat := int(gridDef.Nj)
	nLon := int(gridDef.Ni)

	latStart := float64(gridDef.La1) / 1e6
	latEnd := float64(gridDef.La2) / 1e6
	lonStart := float64(gridDef.Lo1) / 1e6
	lonEnd := float64(gridDef.Lo2) / 1e6

	// Build lat/lon arrays
	lats := make([]float64, nLat)
	lons := make([]float64, nLon)

	if nLat > 1 {
		latStep := (latEnd - latStart) / float64(nLat-1)
		for i := 0; i < nLat; i++ {
			lats[i] = latStart + float64(i)*latStep
		}
	} else {
		lats[0] = latStart
	}

	if nLon > 1 {
		lonStep := (lonEnd - lonStart) / float64(nLon-1)
		for i := 0; i < nLon; i++ {
			lons[i] = lonStart + float64(i)*lonStep
		}
	} else {
		lons[0] = lonStart
	}

	// Reshape data to 2D [lat][lon]
	result := &CDSGridData{
		Variable: variable,
		Year:     year,
		Month:    month,
		Lats:     lats,
		Lons:     lons,
		Values:   make([][]float64, nLat),
	}

	for i := 0; i < nLat; i++ {
		result.Values[i] = make([]float64, nLon)
		for j := 0; j < nLon; j++ {
			idx := i*nLon + j
			if idx < len(data) {
				result.Values[i][j] = float64(data[idx])
			} else {
				result.Values[i][j] = math.NaN()
			}
		}
	}

	return result, nil
}
