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

type MediatorRegistration struct {
	URN                  string          `json:"urn"`
	Version              string          `json:"version"`
	Name                 string          `json:"name"`
	Description          string          `json:"description"`
	DefaultChannelConfig []ChannelConfig `json:"defaultChannelConfig"`
	Endpoints            []Endpoint      `json:"endpoints"`
}

type ChannelConfig struct {
	Name         string     `json:"name"`
	URLPattern   string     `json:"urlPattern"`
	Type         string     `json:"type"`
	AllowedRoles []string   `json:"allow"`
	Routes       []Endpoint `json:"routes"`
}

type Endpoint struct {
	Name    string `json:"name"`
	Host    string `json:"host"`
	Port    int    `json:"port"`
	Path    string `json:"path,omitempty"`
	Primary bool   `json:"primary,omitempty"`
	Type    string `json:"type"`
	Secured bool   `json:"secured,omitempty"`
}

type OpenHIMClient struct {
	cfg  *Config
	http *http.Client
}

func NewOpenHIMClient(cfg *Config) *OpenHIMClient {
	tr := &http.Transport{}
	if cfg.OpenHIMTrustSelf {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return &OpenHIMClient{
		cfg:  cfg,
		http: &http.Client{Transport: tr, Timeout: 30 * time.Second},
	}
}

func (c *OpenHIMClient) Register() error {
	reg := MediatorRegistration{
		URN:         c.cfg.MediatorURN,
		Version:     "0.1.0",
		Name:        "Climate Data Mediator",
		Description: "Pulls climate data from Copernicus CDS and pushes to DHIS2",
		DefaultChannelConfig: []ChannelConfig{
			c.channelConfig("Climate Pull OrgUnit Channel", "^/climate/pull-orgunit.*$", "/climate/pull-orgunit"),
			c.channelConfig("Climate Pull Data Channel", "^/climate/pull-climate.*$", "/climate/pull-climate"),
			c.channelConfig("Climate Push to DHIS2 Channel", "^/climate/push-to-dhis2.*$", "/climate/push-to-dhis2"),
		},
		Endpoints: []Endpoint{
			{Name: "Climate Pull OrgUnit", Host: "localhost", Port: mustAtoi(c.cfg.MediatorPort), Path: "/climate/pull-orgunit", Type: "http"},
			{Name: "Climate Pull Data", Host: "localhost", Port: mustAtoi(c.cfg.MediatorPort), Path: "/climate/pull-climate", Type: "http"},
			{Name: "Climate Push DHIS2", Host: "localhost", Port: mustAtoi(c.cfg.MediatorPort), Path: "/climate/push-to-dhis2", Type: "http"},
		},
	}

	body, _ := json.Marshal(reg)
	req, _ := http.NewRequest("POST", c.cfg.OpenHIMAPIURL+"/mediators", bytes.NewReader(body))
	req.SetBasicAuth(c.cfg.OpenHIMUser, c.cfg.OpenHIMPassword)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("register failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("register returned %d: %s", resp.StatusCode, string(bodyBytes))
	}
	log.Printf("Mediator registered with OpenHIM (URN=%s)", c.cfg.MediatorURN)
	return nil
}

func (c *OpenHIMClient) Heartbeat() {
	ticker := time.NewTicker(10 * time.Second)
	go func() {
		for range ticker.C {
			body := []byte(`{"uptime": 60.5}`)
			url := fmt.Sprintf("%s/mediators/%s/heartbeat", c.cfg.OpenHIMAPIURL, c.cfg.MediatorURN)
			req, _ := http.NewRequest("POST", url, bytes.NewReader(body))
			req.SetBasicAuth(c.cfg.OpenHIMUser, c.cfg.OpenHIMPassword)
			req.Header.Set("Content-Type", "application/json")
			resp, err := c.http.Do(req)
			if err != nil {
				log.Printf("heartbeat error: %v", err)
				continue
			}
			resp.Body.Close()
		}
	}()
}

func (c *OpenHIMClient) channelConfig(name, pattern, path string) ChannelConfig {
	return ChannelConfig{
		Name:         name,
		URLPattern:   pattern,
		Type:         "http",
		AllowedRoles: []string{"climate-sync"},
		Routes: []Endpoint{{
			Name:    name + " Route",
			Host:    c.cfg.MediatorHost,
			Port:    mustAtoi(c.cfg.MediatorPort),
			Path:    path,
			Primary: true,
			Type:    "http",
			Secured: c.cfg.MediatorScheme == "https",
		}},
	}
}

func (c *OpenHIMClient) UpdateTransaction(transactionID string, update map[string]interface{}) error {
	body, _ := json.Marshal(update)
	url := fmt.Sprintf("%s/transactions/%s", c.cfg.OpenHIMAPIURL, transactionID)

	req, err := http.NewRequest("PUT", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.SetBasicAuth(c.cfg.OpenHIMUser, c.cfg.OpenHIMPassword)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("update transaction failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("update transaction returned %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

func (c *OpenHIMClient) updateTransactionFailed(transactionID, mediatorURN, message string) {
	c.UpdateTransaction(transactionID, map[string]interface{}{
		"status": "Failed",
		"response": map[string]interface{}{
			"status":    502,
			"headers":   map[string]string{"Content-Type": "application/json"},
			"body":      fmt.Sprintf(`{"error":%q}`, message),
			"timestamp": time.Now(),
		},
	})
}

func mustAtoi(s string) int {
	var n int
	fmt.Sscanf(s, "%d", &n)
	return n
}
