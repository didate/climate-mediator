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
	requestBody := map[string]interface{}{
		"product_type": []string{productType},
		"variable":     []string{variable},
		"year":         []string{fmt.Sprintf("%d", year)},
		"month":        []string{fmt.Sprintf("%02d", month)},
		"time":         []string{"00:00"},
		"data_format":  "grib",
		"download_format": "unarchived",
		"area":         []float64{13, -15, 7, -7}, // [N, W, S, E]
	}

	body, _ := json.Marshal(requestBody)

	// Submit request
	url := fmt.Sprintf("%s/datasets/%s", c.APIURL, dataset)
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

	// Parse response to get request ID
	var submitResp struct {
		State    string `json:"state"`
		RequestID string `json:"request_id"`
		Location string `json:"location"`
	}
	if err := json.Unmarshal(respBody, &submitResp); err != nil {
		return nil, fmt.Errorf("parse CDS response: %w", err)
	}

	// Poll for completion
	downloadURL, err := c.pollUntilReady(submitResp.RequestID)
	if err != nil {
		return nil, err
	}

	// Download the data
	return c.downloadAndParse(downloadURL, variable, year, month)
}

func (c *CDSClient) pollUntilReady(requestID string) (string, error) {
	url := fmt.Sprintf("%s/tasks/%s", c.APIURL, requestID)

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
			State    string `json:"state"`
			Location string `json:"location"`
			Results  []struct {
				Location string `json:"location"`
			} `json:"result"`
		}
		json.Unmarshal(body, &status)

		log.Printf("CDS request %s: state=%s", requestID, status.State)

		switch status.State {
		case "completed", "successful":
			if len(status.Results) > 0 {
				return status.Results[0].Location, nil
			}
			if status.Location != "" {
				return status.Location, nil
			}
			return "", fmt.Errorf("completed but no download URL")
		case "failed":
			return "", fmt.Errorf("CDS request failed: %s", string(body))
		}
	}

	return "", fmt.Errorf("CDS request timed out after 20 minutes")
}

func (c *CDSClient) downloadAndParse(downloadURL, variable string, year, month int) (*CDSGridData, error) {
	req, _ := http.NewRequest("GET", downloadURL, nil)
	req.Header.Set("PRIVATE-TOKEN", c.APIKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	// Save to temp file for processing
	tmpFile, err := os.CreateTemp("", "cds-*.nc")
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
