package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type HAPIClient struct {
	BaseURL string
	http    *http.Client
}

type FHIRBundle struct {
	ResourceType string        `json:"resourceType"`
	Type         string        `json:"type"`
	Total        int           `json:"total"`
	Link         []BundleLink  `json:"link"`
	Entry        []BundleEntry `json:"entry"`
}

type BundleLink struct {
	Relation string `json:"relation"`
	URL      string `json:"url"`
}

type BundleEntry struct {
	Resource json.RawMessage `json:"resource"`
}

func NewHAPIClient(baseURL string) *HAPIClient {
	return &HAPIClient{
		BaseURL: baseURL,
		http:    &http.Client{Timeout: 60 * time.Second},
	}
}

func (c *HAPIClient) PutLocation(loc *FHIRLocation) error {
	body, _ := json.Marshal(loc)
	url := fmt.Sprintf("%s/Location/%s", c.BaseURL, loc.ID)

	req, err := http.NewRequest("PUT", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/fhir+json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("put location failed: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode >= 300 {
		return fmt.Errorf("put location returned %d", resp.StatusCode)
	}
	return nil
}

func (c *HAPIClient) GetAllLocations(system string) ([]FHIRLocation, error) {
	url := fmt.Sprintf("%s/Location?identifier=%s|&_count=200", c.BaseURL, system)
	var locations []FHIRLocation

	for url != "" {
		bundle, err := c.fetchBundle(url)
		if err != nil {
			return nil, err
		}
		for _, entry := range bundle.Entry {
			var loc FHIRLocation
			if err := json.Unmarshal(entry.Resource, &loc); err == nil {
				locations = append(locations, loc)
			}
		}
		url = nextLink(bundle)
	}

	return locations, nil
}

func (c *HAPIClient) PutObservation(obs *FHIRObservation) error {
	body, _ := json.Marshal(obs)
	url := fmt.Sprintf("%s/Observation/%s", c.BaseURL, obs.ID)

	req, err := http.NewRequest("PUT", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/fhir+json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("put observation failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("put observation returned %d: %s", resp.StatusCode, string(respBody))
	}
	io.Copy(io.Discard, resp.Body)
	return nil
}

func (c *HAPIClient) GetObservations(code, date string) ([]FHIRObservation, error) {
	// Search by system|code and date range (use start of next month as upper bound)
	url := fmt.Sprintf("%s/Observation?code=%s|%s&date=ge%s-01&date=lt%s&_count=200",
		c.BaseURL, cdsSystem, code, date, nextMonth(date))
	var observations []FHIRObservation

	for url != "" {
		bundle, err := c.fetchBundle(url)
		if err != nil {
			return nil, err
		}
		for _, entry := range bundle.Entry {
			var obs FHIRObservation
			if err := json.Unmarshal(entry.Resource, &obs); err == nil {
				observations = append(observations, obs)
			}
		}
		url = nextLink(bundle)
	}

	return observations, nil
}

func (c *HAPIClient) fetchBundle(url string) (*FHIRBundle, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/fhir+json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch bundle failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fhir server returned %d: %s", resp.StatusCode, string(body))
	}

	var bundle FHIRBundle
	if err := json.Unmarshal(body, &bundle); err != nil {
		return nil, fmt.Errorf("parse bundle: %w", err)
	}

	return &bundle, nil
}

// nextMonth returns the first day of the next month given "YYYY-MM".
func nextMonth(yearMonth string) string {
	t, err := time.Parse("2006-01", yearMonth)
	if err != nil {
		return yearMonth + "-28"
	}
	next := t.AddDate(0, 1, 0)
	return next.Format("2006-01-02")
}

func nextLink(b *FHIRBundle) string {
	for _, l := range b.Link {
		if l.Relation == "next" {
			return l.URL
		}
	}
	return ""
}
