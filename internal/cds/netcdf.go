package cds

import (
	"fmt"
	"math"

	"github.com/batchatco/go-native-netcdf/netcdf"
	"github.com/batchatco/go-native-netcdf/netcdf/api"
)

// parseNetCDF parses a NetCDF file and extracts grid data for the given variable.
// reduce combines the file's time steps into one grid: "" keeps the first step
// (monthly means); max, min or mean require one step per day of the month.
func parseNetCDF(path, variable string, year, month int, reduce string) (*CDSGridData, error) {
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

	v, err := nc.GetVariable(varName)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", varName, err)
	}
	steps, err := toFloat64Steps(v.Values)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", varName, err)
	}

	dataVals := steps[0]
	if reduce != "" {
		// A partially published month must not be pushed as if it were complete
		if want := daysInMonth(year, month); len(steps) != want {
			return nil, fmt.Errorf("%s %d-%02d: expected %d daily steps, got %d (month not fully published yet?)", variable, year, month, want, len(steps))
		}
		if dataVals, err = reduceSteps(steps, reduce); err != nil {
			return nil, err
		}
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

	// Reshape the flat [lat*lon] values into [lat][lon]
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

// toFloat64Steps returns the values of each time step as a flat [lat*lon] slice.
// Variables without a time dimension are returned as a single step.
func toFloat64Steps(vals interface{}) ([][]float64, error) {
	switch v := vals.(type) {
	case [][][]float32:
		steps := make([][]float64, len(v))
		for t, grid := range v {
			for _, row := range grid {
				for _, val := range row {
					steps[t] = append(steps[t], float64(val))
				}
			}
		}
		if len(steps) == 0 {
			return nil, fmt.Errorf("empty 3D array")
		}
		return steps, nil
	case [][][]float64:
		steps := make([][]float64, len(v))
		for t, grid := range v {
			for _, row := range grid {
				steps[t] = append(steps[t], row...)
			}
		}
		if len(steps) == 0 {
			return nil, fmt.Errorf("empty 3D array")
		}
		return steps, nil
	default:
		flat, err := toFloat64Slice(vals)
		if err != nil {
			return nil, err
		}
		return [][]float64{flat}, nil
	}
}

// reduceSteps combines time steps cell by cell with max, min or mean. A cell
// missing on any step (e.g. sea in ERA5-Land) stays NaN.
func reduceSteps(steps [][]float64, how string) ([]float64, error) {
	switch how {
	case "max", "min", "mean":
	default:
		return nil, fmt.Errorf("unknown monthly aggregation %q", how)
	}

	out := make([]float64, len(steps[0]))
	for i := range out {
		acc := 0.0
		for t, step := range steps {
			if i >= len(step) || math.IsNaN(step[i]) {
				acc = math.NaN()
				break
			}
			val := step[i]
			switch {
			case t == 0:
				acc = val
			case how == "max":
				acc = math.Max(acc, val)
			case how == "min":
				acc = math.Min(acc, val)
			default:
				acc += val
			}
		}
		if how == "mean" && !math.IsNaN(acc) {
			acc /= float64(len(steps))
		}
		out[i] = acc
	}
	return out, nil
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
