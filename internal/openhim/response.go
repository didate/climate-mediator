package openhim

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type OpenHIMResponse struct {
	XMediatorURN   string            `json:"x-mediator-urn"`
	Status         string            `json:"status"`
	Response       OHResponse        `json:"response"`
	Orchestrations []Orchestration   `json:"orchestrations"`
	Properties     map[string]string `json:"properties,omitempty"`
}

type OHResponse struct {
	Status    int               `json:"status"`
	Headers   map[string]string `json:"headers"`
	Body      string            `json:"body"`
	Timestamp time.Time         `json:"timestamp"`
}

type Orchestration struct {
	Name     string     `json:"name"`
	Request  OHRequest  `json:"request"`
	Response OHResponse `json:"response"`
}

type OHRequest struct {
	Path        string            `json:"path"`
	Headers     map[string]string `json:"headers"`
	Querystring string            `json:"querystring,omitempty"`
	Body        string            `json:"body,omitempty"`
	Method      string            `json:"method"`
	Timestamp   time.Time         `json:"timestamp"`
}

func RespondAccepted(w http.ResponseWriter, mediatorURN, message string) {
	w.Header().Set("Content-Type", "application/json+openhim")
	json.NewEncoder(w).Encode(OpenHIMResponse{
		XMediatorURN: mediatorURN,
		Status:       "Processing",
		Response: OHResponse{
			Status:    202,
			Headers:   map[string]string{"Content-Type": "application/json"},
			Body:      fmt.Sprintf(`{"message":%q}`, message),
			Timestamp: time.Now(),
		},
	})
}

func RespondError(w http.ResponseWriter, mediatorURN string, status int, message string) {
	w.Header().Set("Content-Type", "application/json+openhim")
	json.NewEncoder(w).Encode(OpenHIMResponse{
		XMediatorURN: mediatorURN,
		Status:       "Failed",
		Response: OHResponse{
			Status:    status,
			Headers:   map[string]string{"Content-Type": "application/json"},
			Body:      fmt.Sprintf(`{"error":%q}`, message),
			Timestamp: time.Now(),
		},
	})
}
