package dhis2

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"time"
)

type DHIS2Client struct {
	BaseURL string
	PAT     string
	http    *http.Client
}

func NewDHIS2Client(baseURL, pat string) *DHIS2Client {
	host := ""
	if u, err := url.Parse(baseURL); err == nil {
		host = u.Host
	}
	return &DHIS2Client{
		BaseURL: baseURL,
		PAT:     pat,
		http: &http.Client{
			Timeout: 60 * time.Second,
			Transport: &authTransport{
				pat:  pat,
				host: host,
				base: http.DefaultTransport,
			},
		},
	}
}

// authTransport adds the DHIS2 PAT and a JSON Accept header to every request
// sent to the DHIS2 host. Other hosts (e.g. after a redirect) never get the token.
type authTransport struct {
	pat  string
	host string
	base http.RoundTripper
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// A RoundTripper must not modify the caller's request
	req = req.Clone(req.Context())
	if req.URL.Host == t.host {
		req.Header.Set("Authorization", "ApiToken "+t.pat)
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}
	return t.base.RoundTrip(req)
}

// OrgUnitPageSize is the number of org units requested per DHIS2 page.
// Kept moderate because Polygon/MultiPolygon geometries can be large.
const OrgUnitPageSize = 500

// FetchOrgUnitsWithCoordinates pages through org units that have a geometry and
// calls fn with each page's org units that have usable coordinates (Point,
// Polygon or MultiPolygon). It stops at the first error from DHIS2 or from fn.
// Returns the number of pages fetched.
func (c *DHIS2Client) FetchOrgUnitsWithCoordinates(fn func([]OrgUnit) error) (int, error) {
	for page := 1; ; page++ {
		// order=id:asc keeps paging stable if org units change during the run
		endpoint := fmt.Sprintf("%s/api/organisationUnits?fields=id,name,geometry&filter=geometry:!null&order=id:asc&page=%d&pageSize=%d",
			c.BaseURL, page, OrgUnitPageSize)

		result, err := c.fetchOrgUnitPage(endpoint)
		if err != nil {
			return page - 1, fmt.Errorf("page %d: %w", page, err)
		}

		var filtered []OrgUnit
		for _, ou := range result.OrganisationUnits {
			if ou.Geometry != nil {
				if _, _, ok := ou.Geometry.PointCoordinates(); ok {
					filtered = append(filtered, ou)
				}
			}
		}

		if len(filtered) > 0 {
			if err := fn(filtered); err != nil {
				return page, err
			}
		}

		if page >= result.Pager.PageCount || len(result.OrganisationUnits) == 0 {
			return page, nil
		}
	}
}

type orgUnitPage struct {
	Pager struct {
		Page      int `json:"page"`
		PageCount int `json:"pageCount"`
		Total     int `json:"total"`
	} `json:"pager"`
	OrganisationUnits []OrgUnit `json:"organisationUnits"`
}

// fetchOrgUnitPage GETs one page of org units, retrying up to 3 times on connection errors.
func (c *DHIS2Client) fetchOrgUnitPage(endpoint string) (*orgUnitPage, error) {
	var resp *http.Response
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequest("GET", endpoint, nil)
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}

		resp, err = c.http.Do(req)
		if err == nil {
			break
		}
		if attempt < 2 {
			log.Printf("DHIS2 GET retry %d/3: %v", attempt+1, err)
			time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
			continue
		}
		return nil, fmt.Errorf("fetch org units failed after 3 attempts: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("dhis2 returned %d: %s", resp.StatusCode, string(body))
	}

	var result orgUnitPage
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse org units: %w", err)
	}
	return &result, nil
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
		req.Header.Set("Content-Type", "application/json")

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
