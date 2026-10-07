package cds

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type CDSClient struct {
	APIURL string
	APIKey string
	http   *http.Client
}

// CDSGridData holds the downloaded and parsed climate grid data.
type CDSGridData struct {
	Variable string
	Year     int
	Month    int
	Lats     []float64
	Lons     []float64
	Values   [][]float64 // Values[lat_idx][lon_idx]
}

func NewCDSClient(apiURL, apiKey string) *CDSClient {
	return &CDSClient{
		APIURL: apiURL,
		APIKey: apiKey,
		http:   &http.Client{Timeout: 300 * time.Second},
	}
}

// guineaArea is Guinea's bounding box [N, W, S, E]: lat 7-13°N, lon -15 to -7°W.
var guineaArea = []float64{13, -15, 7, -7}

// FetchMonthlyData requests a monthly means dataset (e.g. reanalysis-era5-land-monthly-means)
// from CDS for Guinea's bounding box, for the specified variable and month.
func (c *CDSClient) FetchMonthlyData(dataset, variable, productType string, year, month int) (*CDSGridData, error) {
	// No grid parameter: data comes at ERA5-Land's native 0.1° resolution (~11 km)
	inputs := map[string]interface{}{
		"product_type": []string{productType},
		"variable":     []string{variable},
		"year":         []string{fmt.Sprintf("%d", year)},
		"month":        []string{fmt.Sprintf("%02d", month)},
		"time":         []string{"00:00"},
		"data_format":  "netcdf",
		"area":         guineaArea,
	}
	return c.fetch(dataset, inputs, variable, year, month, "")
}

// FetchDailyStatistics requests one daily statistic (daily_mean, daily_maximum or
// daily_minimum) for every day of the month from a daily statistics dataset
// (derived-era5-land-daily-statistics), and reduces the days to a single monthly
// grid with monthlyAggregation (max, min or mean).
func (c *CDSClient) FetchDailyStatistics(dataset, variable, statistic, monthlyAggregation string, year, month int) (*CDSGridData, error) {
	return c.fetch(dataset, dailyStatisticsInputs(variable, statistic, year, month), variable, year, month, monthlyAggregation)
}

// dailyStatisticsInputs builds the CDS request inputs for a whole month of daily statistics.
func dailyStatisticsInputs(variable, statistic string, year, month int) map[string]interface{} {
	days := make([]string, daysInMonth(year, month))
	for i := range days {
		days[i] = fmt.Sprintf("%02d", i+1)
	}
	return map[string]interface{}{
		"variable":        []string{variable},
		"year":            fmt.Sprintf("%d", year),
		"month":           fmt.Sprintf("%02d", month),
		"day":             days,
		"daily_statistic": statistic,
		"time_zone":       "utc+00:00", // Guinea is UTC+0, so days match local days
		"frequency":       "1_hourly",
		"area":            guineaArea,
	}
}

// fetch submits a CDS request, waits for it and parses the result. reduce is how
// daily steps are combined into one grid ("" keeps the first step only).
func (c *CDSClient) fetch(dataset string, inputs map[string]interface{}, variable string, year, month int, reduce string) (*CDSGridData, error) {
	body, _ := json.Marshal(map[string]interface{}{"inputs": inputs})

	// Submit request
	// CDS API v1: POST /api/retrieve/v1/processes/{dataset}/execution
	url := fmt.Sprintf("%s/retrieve/v1/processes/%s/execution", c.APIURL, dataset)
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("PRIVATE-TOKEN", c.APIKey)

	log.Printf("Submitting CDS request %s for %s %d-%02d", dataset, variable, year, month)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("submit CDS request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("CDS API returned %d: %s", resp.StatusCode, string(respBody))
	}

	// Parse response to get job ID
	var submitResp struct {
		JobID  string `json:"jobID"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(respBody, &submitResp); err != nil {
		return nil, fmt.Errorf("parse CDS response: %w", err)
	}

	log.Printf("CDS job submitted: %s (status: %s)", submitResp.JobID, submitResp.Status)

	// Poll for completion
	err = c.pollUntilReady(submitResp.JobID)
	if err != nil {
		return nil, err
	}

	// Download the results
	return c.downloadAndParse(submitResp.JobID, variable, year, month, reduce)
}

func (c *CDSClient) pollUntilReady(jobID string) error {
	url := fmt.Sprintf("%s/retrieve/v1/jobs/%s", c.APIURL, jobID)

	// Up to 60 minutes: with parallel requests, jobs can wait in the CDS queue
	for i := 0; i < 360; i++ {
		time.Sleep(10 * time.Second)

		req, _ := http.NewRequest("GET", url, nil)
		req.Header.Set("PRIVATE-TOKEN", c.APIKey)

		resp, err := c.http.Do(req)
		if err != nil {
			log.Printf("Poll error (attempt %d): %v", i+1, err)
			continue
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		var status struct {
			JobID   string `json:"jobID"`
			Status  string `json:"status"`
			Message string `json:"message"`
		}
		json.Unmarshal(body, &status)

		log.Printf("CDS job %s: status=%s", jobID, status.Status)

		switch status.Status {
		case "successful":
			return nil
		case "failed", "rejected", "dismissed":
			return fmt.Errorf("CDS job failed: %s", string(body))
		}
		// "accepted", "running" → keep polling
	}

	return fmt.Errorf("CDS job timed out after 60 minutes")
}

func (c *CDSClient) downloadAndParse(jobID, variable string, year, month int, reduce string) (*CDSGridData, error) {
	// GET /api/retrieve/v1/jobs/{job_id}/results returns JSON with download link
	resultsURL := fmt.Sprintf("%s/retrieve/v1/jobs/%s/results", c.APIURL, jobID)
	req, _ := http.NewRequest("GET", resultsURL, nil)
	req.Header.Set("PRIVATE-TOKEN", c.APIKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get results failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	// Parse the results response to find the download URL
	var results struct {
		Asset struct {
			Value struct {
				Href string `json:"href"`
			} `json:"value"`
		} `json:"asset"`
	}
	// Try parsing as structured response
	if err := json.Unmarshal(body, &results); err == nil && results.Asset.Value.Href != "" {
		return c.downloadFile(results.Asset.Value.Href, variable, year, month, reduce)
	}

	// Try as a map with various structures
	var resultMap map[string]interface{}
	if err := json.Unmarshal(body, &resultMap); err == nil {
		// Look for any href/location/url in the response
		if href := findDownloadURL(resultMap); href != "" {
			return c.downloadFile(href, variable, year, month, reduce)
		}
	}

	// Log the response for debugging
	log.Printf("CDS results response: %s", string(body))
	return nil, fmt.Errorf("could not find download URL in results response")
}

func findDownloadURL(m map[string]interface{}) string {
	for k, v := range m {
		switch val := v.(type) {
		case string:
			if k == "href" || k == "location" || k == "url" {
				return val
			}
		case map[string]interface{}:
			if url := findDownloadURL(val); url != "" {
				return url
			}
		}
	}
	return ""
}

func (c *CDSClient) downloadFile(downloadURL, variable string, year, month int, reduce string) (*CDSGridData, error) {
	log.Printf("Downloading CDS data from: %s", downloadURL)

	req, _ := http.NewRequest("GET", downloadURL, nil)
	req.Header.Set("PRIVATE-TOKEN", c.APIKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	// Save to temp file
	tmpFile, err := os.CreateTemp("", "cds-download-*")
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := io.Copy(tmpFile, resp.Body); err != nil {
		tmpFile.Close()
		return nil, fmt.Errorf("save download: %w", err)
	}
	tmpFile.Close()

	// Check if it's a ZIP file and extract the GRIB
	gribPath, err := extractGribFromZip(tmpFile.Name())
	if err != nil {
		// Not a ZIP, try as raw GRIB
		return parseNetCDF(tmpFile.Name(), variable, year, month, reduce)
	}
	defer os.Remove(gribPath)

	return parseNetCDF(gribPath, variable, year, month, reduce)
}

// extractGribFromZip extracts the first data file from a ZIP archive.
func extractGribFromZip(zipPath string) (string, error) {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", err
	}
	defer r.Close()

	for _, f := range r.File {
		if f.FileInfo().IsDir() {
			continue
		}

		rc, err := f.Open()
		if err != nil {
			return "", fmt.Errorf("open zip entry: %w", err)
		}

		ext := strings.ToLower(filepath.Ext(f.Name))
		tmpFile, err := os.CreateTemp("", "cds-*"+ext)
		if err != nil {
			rc.Close()
			return "", fmt.Errorf("create temp file: %w", err)
		}

		if _, err := io.Copy(tmpFile, rc); err != nil {
			tmpFile.Close()
			rc.Close()
			os.Remove(tmpFile.Name())
			return "", fmt.Errorf("extract file: %w", err)
		}
		tmpFile.Close()
		rc.Close()

		log.Printf("Extracted file: %s from ZIP", f.Name)
		return tmpFile.Name(), nil
	}

	return "", fmt.Errorf("no data file found in ZIP")
}

// GridSample describes how a value was read from the grid for a coordinate,
// so callers can log or audit which cell was used.
type GridSample struct {
	Value      float64
	NearestLat float64 // centre of the grid cell nearest to the coordinate
	NearestLon float64
	CellLat    float64 // centre of the grid cell whose value was used
	CellLon    float64
	Fallback   bool // true when the nearest cell was NaN and a neighbour was used
}

// ExtractValueForCoordinate finds the nearest grid point value for a given lat/lon.
// If the nearest point is NaN (ocean in ERA5-Land), it searches nearby cells
// within a radius of up to 3 grid points.
func (grid *CDSGridData) ExtractValueForCoordinate(lat, lon float64) (float64, bool) {
	s, ok := grid.SampleNearest(lat, lon)
	return s.Value, ok
}

// SampleNearest is ExtractValueForCoordinate, also reporting which cell was used.
func (grid *CDSGridData) SampleNearest(lat, lon float64) (GridSample, bool) {
	if len(grid.Lats) == 0 || len(grid.Lons) == 0 {
		return GridSample{}, false
	}

	latIdx := nearestIndex(grid.Lats, lat)
	lonIdx := nearestIndex(grid.Lons, lon)

	if latIdx < 0 || lonIdx < 0 || latIdx >= len(grid.Values) || lonIdx >= len(grid.Values[latIdx]) {
		return GridSample{}, false
	}

	s := GridSample{
		NearestLat: grid.Lats[latIdx],
		NearestLon: grid.Lons[lonIdx],
	}

	val := grid.Values[latIdx][lonIdx]
	if !math.IsNaN(val) {
		s.Value, s.CellLat, s.CellLon = val, s.NearestLat, s.NearestLon
		return s, true
	}

	// Search nearby cells (expanding radius up to 3 grid points)
	s.Fallback = true
	nLat := len(grid.Lats)
	nLon := len(grid.Lons)
	for radius := 1; radius <= 3; radius++ {
		bestDist := math.MaxFloat64
		bestVal := math.NaN()
		bestI, bestJ := -1, -1
		for di := -radius; di <= radius; di++ {
			for dj := -radius; dj <= radius; dj++ {
				ni := latIdx + di
				nj := lonIdx + dj
				if ni < 0 || ni >= nLat || nj < 0 || nj >= nLon {
					continue
				}
				v := grid.Values[ni][nj]
				if math.IsNaN(v) {
					continue
				}
				dist := math.Sqrt(float64(di*di + dj*dj))
				if dist < bestDist {
					bestDist = dist
					bestVal = v
					bestI, bestJ = ni, nj
				}
			}
		}
		if !math.IsNaN(bestVal) {
			s.Value, s.CellLat, s.CellLon = bestVal, grid.Lats[bestI], grid.Lons[bestJ]
			return s, true
		}
	}

	return s, false
}

func nearestIndex(arr []float64, target float64) int {
	best := 0
	bestDist := math.Abs(arr[0] - target)
	for i := 1; i < len(arr); i++ {
		d := math.Abs(arr[i] - target)
		if d < bestDist {
			bestDist = d
			best = i
		}
	}
	return best
}

// TransformValue applies unit conversion based on the transform type.
// year and month identify the period the value covers; they are needed by
// transforms that scale a daily rate to a monthly total.
func TransformValue(value float64, transform string, year, month int) (float64, string) {
	switch transform {
	case "kelvin_to_celsius":
		return value - 273.15, "Cel"
	case "m_to_mm":
		return value * 1000, "mm"
	case "m_per_day_to_mm_month":
		// ERA5-Land monthly means store accumulations as the mean daily
		// accumulation (m/day); the monthly total is that times the days in the month.
		return value * 1000 * float64(daysInMonth(year, month)), "mm"
	default:
		return value, ""
	}
}

// daysInMonth returns the number of days in the month, accounting for leap years.
func daysInMonth(year, month int) int {
	// Day 0 of the next month is the last day of this month
	return time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// ComputeRelativeHumidity calculates RH from temperature and dewpoint (both in Kelvin).
// Uses the Magnus formula: RH = 100 * exp((17.625 * Td) / (243.04 + Td)) / exp((17.625 * T) / (243.04 + T))
// where T and Td are in Celsius.
func ComputeRelativeHumidity(tempK, dewpointK float64) float64 {
	t := tempK - 273.15
	td := dewpointK - 273.15
	rh := 100 * math.Exp((17.625*td)/(243.04+td)) / math.Exp((17.625*t)/(243.04+t))
	if rh > 100 {
		rh = 100
	}
	if rh < 0 {
		rh = 0
	}
	return rh
}
