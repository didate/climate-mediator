package cds

import (
	"fmt"
	"math"

	"github.com/batchatco/go-native-netcdf/netcdf"
	"github.com/batchatco/go-native-netcdf/netcdf/api"
)

// parseNetCDF parses a NetCDF file and extracts grid data for the given variable.
func parseNetCDF(path, variable string, year, month int) (*CDSGridData, error) {
	nc, err := netcdf.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open netcdf: %w", err)
	}
	defer nc.Close()

	// Read latitude
	lats, err := getFloat64Values(nc, "latitude")
	if err != nil {
		return nil, fmt.Errorf("read latitude: %w", err)
	}

	// Read longitude
	lons, err := getFloat64Values(nc, "longitude")
	if err != nil {
		return nil, fmt.Errorf("read longitude: %w", err)
	}

	// Find the data variable
	varName := findDataVar(nc, variable)
	if varName == "" {
		return nil, fmt.Errorf("variable %q not found in netcdf (available: %v)", variable, nc.ListVariables())
	}

	dataVals, err := getFloat64Values(nc, varName)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", varName, err)
	}

	nLat := len(lats)
	nLon := len(lons)

	grid := &CDSGridData{
		Variable: variable,
		Year:     year,
		Month:    month,
		Lats:     lats,
		Lons:     lons,
		Values:   make([][]float64, nLat),
	}

	// Reshape: skip time dimension if present (take first time step)
	for i := 0; i < nLat; i++ {
		grid.Values[i] = make([]float64, nLon)
		for j := 0; j < nLon; j++ {
			idx := i*nLon + j
			if idx < len(dataVals) {
				grid.Values[i][j] = dataVals[idx]
			} else {
				grid.Values[i][j] = math.NaN()
			}
		}
	}

	return grid, nil
}

// getFloat64Values reads a variable and converts to []float64.
func getFloat64Values(nc api.Group, name string) ([]float64, error) {
	v, err := nc.GetVariable(name)
	if err != nil {
		return nil, err
	}
	return toFloat64Slice(v.Values)
}

// CDS ERA5 variable short names mapping
var cdsShortNames = map[string]string{
	"2m_temperature":          "t2m",
	"2m_dewpoint_temperature": "d2m",
	"total_precipitation":     "tp",
}

func findDataVar(nc api.Group, variable string) string {
	vars := nc.ListVariables()

	// Try exact match
	for _, v := range vars {
		if v == variable {
			return v
		}
	}

	// Try short name
	if short, ok := cdsShortNames[variable]; ok {
		for _, v := range vars {
			if v == short {
				return v
			}
		}
	}

	// Skip dimension variables, return first data variable
	skip := map[string]bool{"latitude": true, "longitude": true, "time": true, "expver": true, "number": true}
	for _, v := range vars {
		if !skip[v] {
			return v
		}
	}

	return ""
}

func toFloat64Slice(vals interface{}) ([]float64, error) {
	switch v := vals.(type) {
	case []float64:
		return v, nil
	case []float32:
		out := make([]float64, len(v))
		for i, val := range v {
			out[i] = float64(val)
		}
		return out, nil
	case []int16:
		out := make([]float64, len(v))
		for i, val := range v {
			out[i] = float64(val)
		}
		return out, nil
	case []int32:
		out := make([]float64, len(v))
		for i, val := range v {
			out[i] = float64(val)
		}
		return out, nil
	// 2D arrays [lat][lon]
	case [][]float32:
		var out []float64
		for _, row := range v {
			for _, val := range row {
				out = append(out, float64(val))
			}
		}
		return out, nil
	case [][]float64:
		var out []float64
		for _, row := range v {
			out = append(out, row...)
		}
		return out, nil
	// 3D arrays [time][lat][lon] — take first time step
	case [][][]float32:
		if len(v) == 0 {
			return nil, fmt.Errorf("empty 3D array")
		}
		var out []float64
		for _, row := range v[0] {
			for _, val := range row {
				out = append(out, float64(val))
			}
		}
		return out, nil
	case [][][]float64:
		if len(v) == 0 {
			return nil, fmt.Errorf("empty 3D array")
		}
		var out []float64
		for _, row := range v[0] {
			out = append(out, row...)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported data type: %T", vals)
	}
}
