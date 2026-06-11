# Climate Data Mediator

An OpenHIM mediator that pulls climate data from the Copernicus Climate Data Store (CDS) API and pushes it to DHIS2, using HAPI FHIR as the central exchange layer.

## Architecture

```
Copernicus CDS (ERA5-Land) ──> HAPI FHIR (Location + Observation) ──> Target DHIS2
                                          ▲
                                     OpenHIM (orchestration & logging)
```

The mediator exposes 3 async endpoints, each tracked as an OpenHIM transaction:

| Endpoint | Description |
|---|---|
| `GET /climate/pull-orgunit` | Fetch org units with coordinates from DHIS2, save as FHIR Locations |
| `GET /climate/pull-climate?months=3` | Download ERA5-Land data from CDS, save as FHIR Observations |
| `GET /climate/push-to-dhis2?months=3` | Read Observations from HAPI, push as dataValues to DHIS2 |

## Climate Variables

Configured via `mapping.json`:

| CDS Variable | DHIS2 Data Element | Transform |
|---|---|---|
| `2m_temperature` | Mean air temperature | Kelvin to Celsius |
| `2m_dewpoint_temperature` | Dewpoint temperature | Kelvin to Celsius |
| `total_precipitation` | Total precipitation | meters to mm |
| `relative_humidity` (computed) | Relative humidity | Magnus formula from T + Td |

## Features

- Async processing: responds 202 immediately, updates OpenHIM transaction when done
- Downloads ERA5-Land monthly means for Guinea's bounding box (one CDS call per variable/month)
- NetCDF format parsing with pure Go (no C dependencies)
- Nearest grid point matching for each org unit's coordinates
- Coastal fallback: searches nearby grid cells when nearest point is ocean (NaN)
- Supports Point, Polygon, and MultiPolygon geometries (centroid for polygons)
- Computed variables: relative humidity derived from temperature + dewpoint
- Configurable variable mapping via `mapping.json`
- Retry logic on DHIS2 connection errors
- Import comments on each data value for traceability

## Configuration

Copy `.env.sample` to `.env` and fill in values:

```bash
cp .env.sample .env
```

Key variables:

| Variable | Description |
|---|---|
| `DHIS2_TARGET_URL` | Target DHIS2 base URL |
| `DHIS2_TARGET_PAT` | Target DHIS2 Personal Access Token |
| `CDS_API_KEY` | Copernicus CDS API key ([get one here](https://cds.climate.copernicus.eu)) |
| `HAPI_FHIR_URL` | HAPI FHIR server URL (e.g. `https://fhir.example.com/fhir`) |
| `OU_IDENTIFIER_SYSTEM` | FHIR Location identifier system (default: `urn:dhis2:entrepot:organisationUnits`) |
| `MAPPING_FILE` | Path to variable mapping JSON (default: `mapping.json`) |
| `MAX_WORKERS` | Concurrent workers (default: `5`) |

See `.env.sample` for the full list.

## Mapping File

The `mapping.json` file defines which CDS variables map to which DHIS2 data elements:

```json
{
  "mappings": [
    {
      "cdsVariable": "2m_temperature",
      "cdsDataset": "reanalysis-era5-land-monthly-means",
      "cdsProductType": "monthly_averaged_reanalysis",
      "dhis2DataElement": "YOUR_DE_ID",
      "dhis2CategoryOptionCombo": "",
      "transform": "kelvin_to_celsius"
    }
  ],
  "computed": [
    {
      "name": "relative_humidity",
      "dhis2DataElement": "YOUR_DE_ID",
      "dhis2CategoryOptionCombo": "",
      "compute": "relative_humidity"
    }
  ]
}
```

Available transforms: `kelvin_to_celsius`, `m_to_mm`

## Running

### Local

```bash
go run .
```

### Docker

```bash
docker compose up -d
```

### Usage

```bash
# Step 1: Pull org units with coordinates to HAPI FHIR
curl "http://localhost:8002/climate/pull-orgunit"

# Step 2: Pull climate data for last 3 months
curl "http://localhost:8002/climate/pull-climate?months=3"

# Step 3: Push to DHIS2
curl "http://localhost:8002/climate/push-to-dhis2?months=3"

# Or for a specific month
curl "http://localhost:8002/climate/pull-climate?year=2026&month=5"
curl "http://localhost:8002/climate/push-to-dhis2?year=2026&month=5"
```

Via OpenHIM:

```bash
curl -u 'client:password' "https://openhim-router.example.com/climate/pull-orgunit"
```

## Prerequisites

- [Copernicus CDS account](https://cds.climate.copernicus.eu) with API key
- Accept the [ERA5-Land licence](https://cds.climate.copernicus.eu/datasets/reanalysis-era5-land-monthly-means?tab=download#manage-licences)
- DHIS2 instance with climate data elements created
- HAPI FHIR R4 server
- OpenHIM with channels configured

## Deployment

- Docker image published to `ghcr.io/didate/climate-mediator:latest`
- GitHub Actions CI/CD: builds on push to `main`, deploys via SSH
- Traefik reverse proxy for HTTPS

## License

MIT
