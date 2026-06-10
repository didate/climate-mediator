# Climate Data Mediator

## Overview
An OpenHIM mediator written in Go that pulls climate data from the Copernicus Climate Data Store (CDS) API and pushes it to DHIS2 as data values. Part of Guinea's health data interoperability platform for climate-health analysis.

## Context
- **Source**: Copernicus CDS API (`cds.climate.copernicus.eu`) — ERA5 reanalysis monthly means
- **Target DHIS2**: National data warehouse (`entrepot.sante.gov.gn/dev`)
- **OpenHIM**: Interoperability layer at `openhim-api.dev.simpetin.com` / `openhim-router.dev.simpetin.com`
- **Data type**: Monthly climate variables (temperature, precipitation, humidity, etc.) mapped to DHIS2 org units via coordinates

## Architecture
- **OpenHIM integration**: Registers as a mediator with URN `urn:mediator:climate-sync`, sends heartbeats. Uses async pattern (responds 202, updates transaction when done).
- **CDS API**: Uses the CDS API with API key authentication to request ERA5 monthly reanalysis data. Returns NetCDF format which is parsed to extract grid values.
- **Coordinate matching**: DHIS2 org units have lat/lon coordinates. CDS data is on a grid. For each org unit, the nearest grid point value is used.
- **Mapping file** (`mapping.json`): Configurable mapping between CDS variables and DHIS2 data elements. Includes unit transformation (Kelvin→Celsius, m→mm, etc.).

## Sync Flow (3 async endpoints, HAPI FHIR as exchange layer)
1. `GET /pull-orgunit` — Fetch org units with coordinates from target DHIS2 → save as FHIR Location (with position) in HAPI
2. `GET /pull-climate?year=2025&month=6` — Read Locations from HAPI → download CDS grid for Guinea → extract nearest values per OU → save as FHIR Observation in HAPI
3. `GET /push-to-dhis2?year=2025&month=6` — Read Observations from HAPI → map via mapping.json → push as dataValues to target DHIS2

## Project Structure
- `main.go` — HTTP server, endpoint registration, OpenHIM response structs
- `config.go` — Environment-based configuration via `godotenv`
- `mapping.go` — Load and parse variable mapping config
- `mapping.json` — CDS variable → DHIS2 data element mapping (configurable)
- `cds.go` — CDS API client (request and download climate data)
- `netcdf.go` — Parse NetCDF data, extract values at coordinates
- `dhis2.go` — DHIS2 API client (fetch org units with coordinates, post data values)
- `openhim.go` — OpenHIM client (registration, heartbeat, transaction update)
- `handler_pull_climate.go` — `/pull-climate` handler
- `handler_push_dhis2.go` — `/push-to-dhis2` handler

## Key Details
- Go module: `github.com/didate/climate-mediator` (Go 1.23)
- CDS API authentication via API key in `CDS_API_KEY` env var
- CDS API returns data asynchronously (submit request → poll status → download)
- Authentication to DHIS2 uses Personal Access Tokens (PAT)
- Authentication to OpenHIM uses HTTP Basic Auth
- DHIS2 org units fetched with coordinates: `/api/organisationUnits?fields=id,name,geometry&paging=false`
- Temporal: monthly data, DHIS2 period format `YYYYMM` (e.g., `202506`)
- Unit transforms: `kelvin_to_celsius` (K - 273.15), `m_to_mm` (* 1000)

## Environment Variables
| Variable | Description |
|---|---|
| `OPENHIM_API_URL` | OpenHIM Core API URL |
| `OPENHIM_API_USER` | OpenHIM API username |
| `OPENHIM_API_PASSWORD` | OpenHIM API password |
| `OPENHIM_TRUST_SELF_SIGNED` | Set `true` to skip TLS verification |
| `MEDIATOR_PORT` | Port (default: `8002`) |
| `MEDIATOR_URN` | URN (default: `urn:mediator:climate-sync`) |
| `MEDIATOR_HOST` | Hostname for OpenHIM route |
| `MEDIATOR_SCHEME` | `http` or `https` |
| `DHIS2_TARGET_URL` | Target DHIS2 base URL |
| `DHIS2_TARGET_PAT` | PAT for target DHIS2 |
| `CDS_API_KEY` | Copernicus CDS API key |
| `CDS_API_URL` | CDS API base URL (default: `https://cds.climate.copernicus.eu/api`) |
| `MAPPING_FILE` | Path to mapping JSON (default: `mapping.json`) |
| `MAX_WORKERS` | Concurrent workers (default: `5`) |

## Deployment
- Docker image via GitHub Actions to ghcr.io
- Same Docker/Traefik infrastructure at `/opt/interop`
- Port 8002 to avoid conflict with dhis2-sync-mediator (8001)
