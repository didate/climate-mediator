package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

type DHIS2Client struct {
	BaseURL string
	PAT     string
	http    *http.Client
}

type OrgUnit struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Geometry *Geometry `json:"geometry,omitempty"`
}

type Geometry struct {
	Type        string          `json:"type"`
	Coordinates json.RawMessage `json:"coordinates"`
}

// PointCoordinates extracts [lon, lat] from any geometry type.
// For Point: returns the coordinates directly.
// For Polygon/MultiPolygon: computes the centroid of the outer ring.
func (g *Geometry) PointCoordinates() (lon, lat float64, ok bool) {
	switch g.Type {
	case "Point":
		var coords []float64
		if err := json.Unmarshal(g.Coordinates, &coords); err != nil || len(coords) < 2 {
			return 0, 0, false
		}
		return coords[0], coords[1], true

	case "Polygon":
		// [[[lon,lat], [lon,lat], ...]]
		var rings [][][]float64
		if err := json.Unmarshal(g.Coordinates, &rings); err != nil || len(rings) == 0 {
			return 0, 0, false
		}
		return centroid(rings[0])

	case "MultiPolygon":
		// [[[[lon,lat], [lon,lat], ...]]]
		var polys [][][][]float64
		if err := json.Unmarshal(g.Coordinates, &polys); err != nil || len(polys) == 0 || len(polys[0]) == 0 {
			return 0, 0, false
		}
		return centroid(polys[0][0])

	default:
		return 0, 0, false
	}
}

// centroid computes the centroid of a polygon ring.
func centroid(ring [][]float64) (lon, lat float64, ok bool) {
	if len(ring) == 0 {
		return 0, 0, false
	}
	var sumLon, sumLat float64
	for _, pt := range ring {
		if len(pt) < 2 {
			continue
		}
		sumLon += pt[0]
		sumLat += pt[1]
	}
	n := float64(len(ring))
	return sumLon / n, sumLat / n, true
}

type DataValueSet struct {
	DataSet    string      `json:"dataSet,omitempty"`
	Period     string      `json:"period,omitempty"`
	OrgUnit    string      `json:"orgUnit,omitempty"`
	DataValues []DataValue `json:"dataValues"`
}

type DataValue struct {
	DataElement          string `json:"dataElement"`
	Period               string `json:"period,omitempty"`
	OrgUnit              string `json:"orgUnit,omitempty"`
	CategoryOptionCombo  string `json:"categoryOptionCombo,omitempty"`
	Value                string `json:"value"`
}

type ImportCount struct {
	Imported int `json:"imported"`
	Updated  int `json:"updated"`
	Ignored  int `json:"ignored"`
	Deleted  int `json:"deleted"`
}

func NewDHIS2Client(baseURL, pat string) *DHIS2Client {
	return &DHIS2Client{
		BaseURL: baseURL,
		PAT:     pat,
		http: &http.Client{
			Timeout: 60 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: false},
			},
		},
	}
}

// FetchOrgUnitsWithCoordinates fetches org units that have geometry (coordinates).
func (c *DHIS2Client) FetchOrgUnitsWithCoordinates() ([]OrgUnit, error) {
	endpoint := fmt.Sprintf("%s/api/organisationUnits?fields=id,name,geometry&filter=geometry:!null&paging=false", c.BaseURL)

	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "ApiToken "+c.PAT)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch org units failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("dhis2 returned %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		OrganisationUnits []OrgUnit `json:"organisationUnits"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse org units: %w", err)
	}

	// Filter to OUs with valid coordinates (Point, Polygon, or MultiPolygon)
	var filtered []OrgUnit
	for _, ou := range result.OrganisationUnits {
		if ou.Geometry != nil {
			if _, _, ok := ou.Geometry.PointCoordinates(); ok {
				filtered = append(filtered, ou)
			}
		}
	}

	return filtered, nil
}

func (c *DHIS2Client) PostDataValueSet(dvs *DataValueSet) ([]byte, string, error) {
	endpoint := fmt.Sprintf("%s/api/dataValueSets", c.BaseURL)

	body, err := json.Marshal(dvs)
	if err != nil {
		return nil, endpoint, fmt.Errorf("marshal dataValueSet: %w", err)
	}

	// Retry up to 3 times on connection errors (EOF, timeout)
	var resp *http.Response
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequest("POST", endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, endpoint, fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("Authorization", "ApiToken "+c.PAT)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")

		resp, err = c.http.Do(req)
		if err == nil {
			break
		}
		if attempt < 2 {
			log.Printf("DHIS2 POST retry %d/3: %v", attempt+1, err)
			time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
			continue
		}
		return nil, endpoint, fmt.Errorf("dhis2 call failed after 3 attempts: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, endpoint, fmt.Errorf("read body: %w", err)
	}

	if resp.StatusCode >= 300 {
		return respBody, endpoint, fmt.Errorf("dhis2 returned %d: %s", resp.StatusCode, string(respBody))
	}

	return respBody, endpoint, nil
}

func parseImportCount(body []byte) *ImportCount {
	var resp struct {
		Response struct {
			ImportCount ImportCount `json:"importCount"`
		} `json:"response"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return &ImportCount{}
	}
	return &resp.Response.ImportCount
}
