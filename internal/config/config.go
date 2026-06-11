package config

import (
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

type Config struct {
	OpenHIMAPIURL    string
	OpenHIMUser      string
	OpenHIMPassword  string
	OpenHIMTrustSelf bool
	MediatorPort     string
	MediatorURN      string
	MediatorHost     string
	MediatorScheme   string
	DHIS2TargetURL   string
	DHIS2TargetPAT   string
	CDSAPIKey        string
	CDSAPIURL        string
	HAPIFhirURL        string
	OUIdentifierSystem string
	MappingFile        string
	MaxWorkers       int
}

func LoadConfig() *Config {
	_ = godotenv.Load()
	return &Config{
		OpenHIMAPIURL:    os.Getenv("OPENHIM_API_URL"),
		OpenHIMUser:      os.Getenv("OPENHIM_API_USER"),
		OpenHIMPassword:  os.Getenv("OPENHIM_API_PASSWORD"),
		OpenHIMTrustSelf: os.Getenv("OPENHIM_TRUST_SELF_SIGNED") == "true",
		MediatorPort:     getEnvDefault("MEDIATOR_PORT", "8002"),
		MediatorURN:      getEnvDefault("MEDIATOR_URN", "urn:mediator:climate-sync"),
		MediatorHost:     getEnvDefault("MEDIATOR_HOST", "localhost"),
		MediatorScheme:   getEnvDefault("MEDIATOR_SCHEME", "http"),
		DHIS2TargetURL:   os.Getenv("DHIS2_TARGET_URL"),
		DHIS2TargetPAT:   os.Getenv("DHIS2_TARGET_PAT"),
		CDSAPIKey:        os.Getenv("CDS_API_KEY"),
		CDSAPIURL:        getEnvDefault("CDS_API_URL", "https://cds.climate.copernicus.eu/api"),
		HAPIFhirURL:        os.Getenv("HAPI_FHIR_URL"),
		OUIdentifierSystem: getEnvDefault("OU_IDENTIFIER_SYSTEM", "urn:dhis2:entrepot:organisationUnits"),
		MappingFile:      getEnvDefault("MAPPING_FILE", "mapping.json"),
		MaxWorkers:       getEnvDefaultInt("MAX_WORKERS", 5),
	}
}

func getEnvDefault(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func getEnvDefaultInt(k string, def int) int {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}
