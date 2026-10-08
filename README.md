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
| `total_precipitation` | Monthly total precipitation (mm) | mean daily m → monthly mm (× 1000 × days in month) |
| `2m_temperature` (daily maximum) | Monthly maximum air temperature | Kelvin to Celsius, highest daily maximum of the month |
| `2m_temperature` (daily minimum) | Monthly minimum air temperature | Kelvin to Celsius, lowest daily minimum of the month |
| `relative_humidity` (computed) | Relative humidity | Magnus formula from T + Td |

Monthly mean variables come from `reanalysis-era5-land-monthly-means`. Tmax/Tmin come from
`derived-era5-land-daily-statistics` (one value per day, UTC+0), reduced to the month with
`monthlyAggregation`. A month is only processed once every day is published.

## Features

- Async processing: responds 202 immediately, updates OpenHIM transaction when done
- Downloads ERA5-Land monthly means for Guinea's bounding box (one CDS call per variable/month)
- NetCDF format parsing with pure Go (no C dependencies)
- Nearest grid point matching for each org unit's coordinates
- Coastal fallback: searches nearby grid cells when nearest point is ocean (NaN)
- Supports Point, Polygon, and MultiPolygon geometries: polygons use their area-weighted centroid (all parts, holes subtracted), or a point inside the shape when the centroid falls outside it
- Stores polygon boundaries on the FHIR Location (`location-boundary-geojson` extension) for future zonal statistics
- Logs the grid cell used per org unit, and warns when several org units share a cell through the coastal fallback
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
| `HAPI_FHIR_URL` | HAPI FHIR server URL (internal, e.g. `http://hapi:8080/fhir` on the `interop_internal` network) |
| `OU_IDENTIFIER_SYSTEM` | FHIR Location identifier system (default: `urn:dhis2:entrepot:organisationUnits`) |
| `LOCATION_ID_PREFIX` | HAPI Location IDs are `<prefix>-<orgUnitUID>` so they never collide with other mediators sharing HAPI (default: `entrepot`) |
| `MAPPING_FILE` | Path to variable mapping JSON (default: `mapping.json`) |
| `MAX_WORKERS` | Concurrent workers (default: `5`) |
| `CDS_MAX_PARALLEL` | Maximum number of CDS requests in flight at the same time (default: `4`) |
| `CDS_JOB_TIMEOUT_MINUTES` | Maximum wait for one CDS job, queue included (default: `120`). Transient failures (5xx, timeout) are retried twice |
| `DEFAULT_MONTHS` | Past months processed when no period param is given (default: `1` = previous month) |

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

Mapping fields for daily statistics datasets:

| Field | Values | Purpose |
|---|---|---|
| `name` | e.g. `2m_temperature_max` | Unique identifier when several mappings share a `cdsVariable` (used as the Observation code) |
| `dailyStatistic` | `daily_mean`, `daily_maximum`, `daily_minimum` | Requests the daily statistics dataset instead of monthly means |
| `monthlyAggregation` | `max`, `min`, `mean` | How the days are reduced to the month (e.g. `mean` for the mean of daily maxima) |

Available transforms:

| Transform | Formula | Use |
|---|---|---|
| `kelvin_to_celsius` | K − 273.15 | Temperatures |
| `m_to_mm` | m × 1000 | Accumulations already covering the target period (hourly/daily data) |
| `m_per_day_to_mm_month` | m/day × 1000 × days in month | Accumulations from ERA5-Land *monthly means*, which are mean daily values ([ECMWF conversion table](https://confluence.ecmwf.int/spaces/CKB/pages/197702790/Conversion+table+for+accumulated+variables+total+precipitation+fluxes)) |

## Running

### Local

```bash
go run ./cmd/server
```

### Docker

```bash
docker compose up -d
```

### Usage

```bash
# Step 1: Pull org units with coordinates to HAPI FHIR
curl "http://localhost:8002/climate/pull-orgunit"

# Step 2: Pull climate data (default: previous month, see DEFAULT_MONTHS)
curl "http://localhost:8002/climate/pull-climate"

# Step 3: Push to DHIS2
curl "http://localhost:8002/climate/push-to-dhis2"

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
