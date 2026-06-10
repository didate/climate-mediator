package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
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

// FetchMonthlyData requests monthly climate data from CDS for Guinea's bounding box.
// It downloads a CSV/JSON formatted result for the specified variable and month.
func (c *CDSClient) FetchMonthlyData(dataset, variable, productType string, year, month int) (*CDSGridData, error) {
	// Guinea bounding box: lat 7-13°N, lon -15 to -7°W
	// Using 0.25° grid resolution
	// CDS API v1: POST /api/retrieve/v1/processes/{dataset}/execution
	requestBody := map[string]interface{}{
		"inputs": map[string]interface{}{
			"product_type": []string{productType},
			"variable":     []string{variable},
			"year":         []string{fmt.Sprintf("%d", year)},
			"month":        []string{fmt.Sprintf("%02d", month)},
			"time":         []string{"00:00"},
			"data_format":  "grib",
			"area":         []float64{13, -15, 7, -7}, // [N, W, S, E]
		},
	}

	body, _ := json.Marshal(requestBody)

	// Submit request
	url := fmt.Sprintf("%s/retrieve/v1/processes/%s/execution", c.APIURL, dataset)
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("PRIVATE-TOKEN", c.APIKey)

	log.Printf("Submitting CDS request for %s %d-%02d", variable, year, month)

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
	return c.downloadAndParse(submitResp.JobID, variable, year, month)
}

func (c *CDSClient) pollUntilReady(jobID string) error {
	url := fmt.Sprintf("%s/retrieve/v1/jobs/%s", c.APIURL, jobID)

	for i := 0; i < 120; i++ { // Max 20 minutes
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

	return fmt.Errorf("CDS job timed out after 20 minutes")
}

func (c *CDSClient) downloadAndParse(jobID, variable string, year, month int) (*CDSGridData, error) {
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
		return c.downloadFile(results.Asset.Value.Href, variable, year, month)
	}

	// Try as a map with various structures
	var resultMap map[string]interface{}
	if err := json.Unmarshal(body, &resultMap); err == nil {
		// Look for any href/location/url in the response
		if href := findDownloadURL(resultMap); href != "" {
			return c.downloadFile(href, variable, year, month)
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

func (c *CDSClient) downloadFile(downloadURL, variable string, year, month int) (*CDSGridData, error) {
	log.Printf("Downloading CDS data from: %s", downloadURL)

	req, _ := http.NewRequest("GET", downloadURL, nil)
	req.Header.Set("PRIVATE-TOKEN", c.APIKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	// Save to temp file for processing
	tmpFile, err := os.CreateTemp("", "cds-*.grib")
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := io.Copy(tmpFile, resp.Body); err != nil {
		tmpFile.Close()
		return nil, fmt.Errorf("save download: %w", err)
	}
	tmpFile.Close()

	return parseNetCDF(tmpFile.Name(), variable, year, month)
}

// ExtractValueForCoordinate finds the nearest grid point value for a given lat/lon.
func (grid *CDSGridData) ExtractValueForCoordinate(lat, lon float64) (float64, bool) {
	if len(grid.Lats) == 0 || len(grid.Lons) == 0 {
		return 0, false
	}

	latIdx := nearestIndex(grid.Lats, lat)
	lonIdx := nearestIndex(grid.Lons, lon)

	if latIdx < 0 || lonIdx < 0 || latIdx >= len(grid.Values) || lonIdx >= len(grid.Values[latIdx]) {
		return 0, false
	}

	val := grid.Values[latIdx][lonIdx]
	if math.IsNaN(val) {
		return 0, false
	}

	return val, true
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
func TransformValue(value float64, transform string) (float64, string) {
	switch transform {
	case "kelvin_to_celsius":
		return value - 273.15, "Cel"
	case "m_to_mm":
		return value * 1000, "mm"
	default:
		return value, ""
	}
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
