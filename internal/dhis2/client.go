package dhis2

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

func ParseImportCount(body []byte) *ImportCount {
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
