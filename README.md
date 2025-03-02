# Meshtastic Tile Downloader (Go version)

This is a Go implementation of the Meshtastic Tile Downloader, a tool for downloading map tiles for offline use with Meshtastic devices.

## Features

- Downloads map tiles from various providers (Thunderforest, Geoapify, CNIG.es)
- Configurable via YAML file
- Supports multiple zones with different zoom levels
- Progress tracking during download
- Image optimization for higher zoom levels
- Skips already downloaded tiles

## Installation

### Prerequisites

- Go 1.16 or higher
- Dependencies:
    - github.com/schollz/progressbar/v3
    - gopkg.in/yaml.v3

### Building

```bash
# Install dependencies
go get github.com/schollz/progressbar/v3
go get gopkg.in/yaml.v3

# Build the application
go build -o meshtastic-tile-downloader
```

## Usage

1. Create a `config.yaml` file with your desired zones and map settings (see example below)
2. Set the required environment variables:
    - `API_KEY` or `[PROVIDER]_API_KEY` (e.g., `THUNDERFOREST_API_KEY`) with your API key
    - `DOWNLOAD_DIRECTORY` (optional, defaults to `~/Desktop/maps`)
    - `DEBUG` (optional, set to "true" to enable debug mode)
3. Run the application

```bash
# Run with default settings
./meshtastic-tile-downloader

# Run with custom environment variables
DOWNLOAD_DIRECTORY=/path/to/maps THUNDERFOREST_API_KEY=your_api_key ./meshtastic-tile-downloader
```

### Example Configuration

```yaml
zones:
  Europe:
    regions:
     - 30.0,-15.0,60.0,50.8
    zoom:
      out: 1
      in: 8
  Vigo:
    zoom:
      out: 10
      in: 16
    regions:
      - 42.24285,-8.78276,42.20617,-8.67122
map:
  style: atlas
  provider: thunderforest
  reduce: 12
```

## Configuration Format

### Zones

Each zone contains:
- `regions`: List of regions defined by coordinates in the format "minLat,minLon,maxLat,maxLon"
- `zoom`: Zoom level range
    - `in`: Closest zoom level (higher number = more detail)
    - `out`: Furthest zoom level (lower number = less detail)

### Map

- `provider`: Map provider (thunderforest, geoapify, cnig.es)
- `style`: Map style (depends on provider, e.g., "atlas" for Thunderforest)
- `reduce`: Zoom level at which to start optimizing images (higher value = less optimization)

## Credits

Based on the Python implementation by:
- @droberin (Meshtastic Spain community) https://gist.github.com/droberin/b333a216d860361e329e74f59f4af4ba
- @pcamelo (Meshtastic Portugal community)

Map providers:
- Thunderforest: Maps © www.thunderforest.com, Data © www.osm.org/copyright
- Geoapify
- CNIG.es (Spain)

## License

Same as the original Python implementation.
