package main

import (
	"bytes"
	"fmt"
	"image"
	_ "image/jpeg" // Register JPEG decoder
	"image/png"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/schollz/progressbar/v3"
	"gopkg.in/yaml.v3"
)

/*
Meshtastic Tile Downloader - Go Version
Based on the Python implementation by:
  - DRoBeR (Meshtastic Spain community)
  - pcamelo (Meshtastic Portugal community)
  - Find them via LoRa Channel: Iberia. (or Telegram communities)

Map providers:
  - Thunderforest: Maps © www.thunderforest.com, Data © www.osm.org/copyright
  - Geoapify
  - CNIG.es
*/

// Config represents the YAML configuration structure
type Config struct {
	Zones map[string]Zone `yaml:"zones"`
	Map   MapConfig       `yaml:"map"`
}

// Zone represents a geographical zone with regions and zoom levels
type Zone struct {
	Regions []string `yaml:"regions"`
	Zoom    struct {
		In  int `yaml:"in"`
		Out int `yaml:"out"`
	} `yaml:"zoom"`
}

// MapConfig represents map provider configuration
type MapConfig struct {
	Provider string `yaml:"provider"`
	Style    string `yaml:"style"`
	Reduce   int    `yaml:"reduce"`
}

// MeshtasticTileDownloader is the main application struct
type MeshtasticTileDownloader struct {
	config          Config
	outputDirectory string
	apiKey          string
}

// NewMeshtasticTileDownloader creates a new tile downloader
func NewMeshtasticTileDownloader(outputDirectory string) *MeshtasticTileDownloader {
	return &MeshtasticTileDownloader{
		outputDirectory: outputDirectory,
	}
}

// LoadConfig loads configuration from a YAML file
func (m *MeshtasticTileDownloader) LoadConfig(configFile string) error {
	data, err := os.ReadFile(configFile)
	if err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}

	if err := yaml.Unmarshal(data, &m.config); err != nil {
		return fmt.Errorf("failed to parse YAML: %w", err)
	}

	return nil
}

// ValidateConfig validates the configuration
func (m *MeshtasticTileDownloader) ValidateConfig() bool {
	log.Println("Analysing configuration.")

	// Check zones
	log.Printf("Found %d zones", len(m.config.Zones))
	for zoneName, zone := range m.config.Zones {
		log.Printf("[%s] contains %d regions", zoneName, len(zone.Regions))

		// Set default zoom levels if not specified
		modified := false
		if zone.Zoom.In == 0 {
			zone.Zoom.In = 8
			modified = true
			log.Printf("Setting default zoom in level for [%s] to 8", zoneName)
		}
		if zone.Zoom.Out == 0 {
			zone.Zoom.Out = 1
			modified = true
			log.Printf("Setting default zoom out level for [%s] to 1", zoneName)
		}

		// If we modified the zone, update it in the map
		if modified {
			m.config.Zones[zoneName] = zone
		}
	}

	// Set map defaults if needed
	if m.config.Map.Provider == "" {
		m.config.Map.Provider = "thunderforest"
		log.Println("Setting default provider to thunderforest")
	}
	if m.config.Map.Style == "" {
		m.config.Map.Style = "atlas"
		log.Println("Setting default style to atlas")
	}
	if m.config.Map.Reduce == 0 {
		m.config.Map.Reduce = 12
		log.Println("Setting default reduce level to 12")
	} else if m.config.Map.Reduce < 1 || m.config.Map.Reduce > 16 {
		m.config.Map.Reduce = 100
		log.Println("Setting reduce level to 100 due to out-of-range value")
	}

	// Validate provider
	if !m.IsValidProvider() {
		knownProviders := strings.Join(m.KnownProviders(), ", ")
		log.Printf("Provider '%s' is unknown. Known: '%s'", m.config.Map.Provider, knownProviders)
		return false
	}

	return true
}

// TileProvider returns the configured tile provider
func (m *MeshtasticTileDownloader) TileProvider() string {
	return m.config.Map.Provider
}

// MapStyle returns the configured map style
func (m *MeshtasticTileDownloader) MapStyle() string {
	return m.config.Map.Style
}

// IsValidProvider checks if the provider is valid
func (m *MeshtasticTileDownloader) IsValidProvider() bool {
	_, ok := m.GetTileProviderURLTemplate()[m.TileProvider()]
	return ok
}

// KnownProviders returns a list of known providers
func (m *MeshtasticTileDownloader) KnownProviders() []string {
	providers := make([]string, 0, len(m.GetTileProviderURLTemplate()))
	for k := range m.GetTileProviderURLTemplate() {
		providers = append(providers, k)
	}
	return providers
}

// GetTileProviderURLTemplate returns a map of provider URL templates
func (m *MeshtasticTileDownloader) GetTileProviderURLTemplate() map[string]string {
	return map[string]string{
		"thunderforest": "https://tile.thunderforest.com/{{MAP_STYLE}}/{{ZOOM}}/{{X}}/{{Y}}.png?apikey={{API_KEY}}",
		"geoapify":      "https://maps.geoapify.com/v1/tile/{{MAP_STYLE}}/{{ZOOM}}/{{X}}/{{Y}}.png?apiKey={{API_KEY}}",
		"cnig.es":       "https://tms-ign-base.idee.es/1.0.0/IGNBaseTodo/{{ZOOM}}/{{X}}/{{Y}}.jpeg",
	}
}

// ParseURL parses a URL template with the given parameters
func (m *MeshtasticTileDownloader) ParseURL(zoom, x, y int) string {
	url := m.GetTileProviderURLTemplate()[m.TileProvider()]
	url = strings.Replace(url, "{{MAP_STYLE}}", m.MapStyle(), -1)
	url = strings.Replace(url, "{{ZOOM}}", strconv.Itoa(zoom), -1)
	url = strings.Replace(url, "{{X}}", strconv.Itoa(x), -1)
	url = strings.Replace(url, "{{Y}}", strconv.Itoa(y), -1)
	url = strings.Replace(url, "{{API_KEY}}", m.apiKey, -1)
	return url
}

// RedactKey redacts the API key in a URL for logging
func (m *MeshtasticTileDownloader) RedactKey(url string) string {
	if m.apiKey != "" {
		return strings.Replace(url, m.apiKey, "[REDACTED]", -1)
	}
	return url
}

// LongToTileX converts longitude to tile X coordinate
func (m *MeshtasticTileDownloader) LongToTileX(lon float64, zoom int) int {
	xyTilesCount := math.Pow(2, float64(zoom))
	return int(math.Floor(((lon + 180.0) / 360.0) * xyTilesCount))
}

// LatToTileY converts latitude to tile Y coordinate
func (m *MeshtasticTileDownloader) LatToTileY(lat float64, zoom int) int {
	xyTilesCount := math.Pow(2, float64(zoom))
	return int(math.Floor(((1.0 - math.Log(math.Tan((lat*math.Pi)/180.0)+1.0/math.Cos((lat*math.Pi)/180.0))/math.Pi) / 2.0) * xyTilesCount))
}

// IsInDebugMode checks if debug mode is enabled
func (m *MeshtasticTileDownloader) IsInDebugMode() bool {
	debug := os.Getenv("DEBUG")
	return strings.ToLower(debug) != "false" && debug != ""
}

// LoadImageBytes loads and returns an image from bytes
func (m *MeshtasticTileDownloader) LoadImageBytes(imgData []byte) (image.Image, error) {
	img, _, err := image.Decode(bytes.NewReader(imgData))
	if err != nil {
		return nil, fmt.Errorf("failed to decode image: %w", err)
	}
	return img, nil
}

// DownloadTile downloads a single tile
func (m *MeshtasticTileDownloader) DownloadTile(zoom, x, y int) error {
	reducing := zoom >= m.config.Map.Reduce
	url := m.ParseURL(zoom, x, y)
	redactedURL := m.RedactKey(url)

	// Determine the output path
	tileDir := filepath.Join(m.outputDirectory, m.TileProvider(), m.MapStyle(), strconv.Itoa(zoom), strconv.Itoa(x))
	tilePath := filepath.Join(tileDir, fmt.Sprintf("%d.png", y))

	// Create directories if they don't exist
	if err := os.MkdirAll(tileDir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Skip if file already exists
	if _, err := os.Stat(tilePath); err == nil {
		log.Printf("[%s] file already exists. Skipping... %s", tilePath, redactedURL)
		return nil
	}

	// Skip download in debug mode
	if m.IsInDebugMode() {
		log.Printf("DEBUG IS ACTIVE: not obtaining tile: %s (Would reduce: %v)", redactedURL, reducing)
		return nil
	}

	// Download the tile
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("failed to download: %w", err)
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {

		}
	}(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to download tile %d/%d/%d: %d %s", zoom, x, y, resp.StatusCode, resp.Status)
	}

	contentType := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "image/") {
		return fmt.Errorf("failed to parse tile %d/%d/%d: %d: not an image", zoom, x, y, resp.StatusCode)
	}

	// Read the image data
	imgData, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response body: %w", err)
	}

	// Process and save the image
	if reducing {
		log.Printf("Reducing tile from %s → %s", redactedURL, tilePath)
		return m.ReduceTile(imgData, tilePath)
	}

	log.Printf("Saving not altered tile %s → %s", redactedURL, tilePath)
	if contentType != "image/png" {
		return m.SaveConvertedTile(imgData, tilePath)
	}

	return os.WriteFile(tilePath, imgData, 0644)
}

// ReduceTile reduces the color depth of an image
func (m *MeshtasticTileDownloader) ReduceTile(imgData []byte, destination string) error {
	img, err := m.LoadImageBytes(imgData)
	if err != nil {
		return err
	}

	// Save the image with compression
	f, err := os.Create(destination)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer func(f *os.File) {
		err := f.Close()
		if err != nil {

		}
	}(f)

	// Use png encoder with best compression
	encoder := png.Encoder{
		CompressionLevel: png.BestCompression,
	}
	return encoder.Encode(f, img)
}

// SaveConvertedTile saves a tile converted to PNG
func (m *MeshtasticTileDownloader) SaveConvertedTile(imgData []byte, destination string) error {
	img, err := m.LoadImageBytes(imgData)
	if err != nil {
		return err
	}

	f, err := os.Create(destination)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer func(f *os.File) {
		err := f.Close()
		if err != nil {

		}
	}(f)

	// Use png encoder with best compression
	encoder := png.Encoder{
		CompressionLevel: png.BestCompression,
	}
	return encoder.Encode(f, img)
}

// ObtainTiles downloads all tiles for the given regions and zoom levels
func (m *MeshtasticTileDownloader) ObtainTiles(regions []string, zoomLevels []int) error {
	totalTiles := 0

	// Calculate total tiles first for progress bar
	for _, zoom := range zoomLevels {
		for _, region := range regions {
			coords := strings.Split(region, ",")
			if len(coords) != 4 {
				return fmt.Errorf("invalid region format: %s", region)
			}

			minLat, err := strconv.ParseFloat(coords[0], 64)
			if err != nil {
				return fmt.Errorf("invalid latitude: %w", err)
			}
			minLon, err := strconv.ParseFloat(coords[1], 64)
			if err != nil {
				return fmt.Errorf("invalid longitude: %w", err)
			}
			maxLat, err := strconv.ParseFloat(coords[2], 64)
			if err != nil {
				return fmt.Errorf("invalid latitude: %w", err)
			}
			maxLon, err := strconv.ParseFloat(coords[3], 64)
			if err != nil {
				return fmt.Errorf("invalid longitude: %w", err)
			}

			startX := m.LongToTileX(minLon, zoom)
			endX := m.LongToTileX(maxLon, zoom)
			startY := m.LatToTileY(maxLat, zoom)
			endY := m.LatToTileY(minLat, zoom)

			minX := int(math.Min(float64(startX), float64(endX)))
			maxX := int(math.Max(float64(startX), float64(endX)))
			minY := int(math.Min(float64(startY), float64(endY)))
			maxY := int(math.Max(float64(startY), float64(endY)))

			tilesX := maxX - minX + 1
			tilesY := maxY - minY + 1
			totalTiles += tilesX * tilesY
		}
	}

	// Create progress bar
	bar := progressbar.Default(int64(totalTiles), "Downloading tiles")

	// Download tiles
	for _, zoom := range zoomLevels {
		for _, region := range regions {
			coords := strings.Split(region, ",")

			minLat, _ := strconv.ParseFloat(coords[0], 64)
			minLon, _ := strconv.ParseFloat(coords[1], 64)
			maxLat, _ := strconv.ParseFloat(coords[2], 64)
			maxLon, _ := strconv.ParseFloat(coords[3], 64)

			startX := m.LongToTileX(minLon, zoom)
			endX := m.LongToTileX(maxLon, zoom)
			startY := m.LatToTileY(maxLat, zoom)
			endY := m.LatToTileY(minLat, zoom)

			minX := int(math.Min(float64(startX), float64(endX)))
			maxX := int(math.Max(float64(startX), float64(endX)))
			minY := int(math.Min(float64(startY), float64(endY)))
			maxY := int(math.Max(float64(startY), float64(endY)))

			for x := minX; x <= maxX; x++ {
				for y := minY; y <= maxY; y++ {
					if err := m.DownloadTile(zoom, x, y); err != nil {
						log.Printf("Error downloading tile %d/%d/%d: %v", zoom, x, y, err)
					}
					_ = bar.Add(1)
				}
			}
		}
	}

	return nil
}

// Run executes the tile download process for all configured zones
func (m *MeshtasticTileDownloader) Run() bool {
	if !m.IsValidProvider() {
		log.Printf("Unknown provider '%s'", m.TileProvider())
		return false
	}

	for zoneName, zone := range m.config.Zones {
		// Create zoom level range
		zoomLevels := make([]int, 0, zone.Zoom.In-zone.Zoom.Out)
		for i := zone.Zoom.Out; i < zone.Zoom.In; i++ {
			zoomLevels = append(zoomLevels, i)
		}

		log.Printf("Obtaining zone [%s] [zoom: %d → %d] regions: %v",
			zoneName, zone.Zoom.Out, zone.Zoom.In, zone.Regions)

		if err := m.ObtainTiles(zone.Regions, zoomLevels); err != nil {
			log.Printf("Error obtaining tiles for zone %s: %v", zoneName, err)
			return false
		}

		log.Printf("Finished with zone %s", zoneName)
	}

	// List all processed zones
	zoneNames := make([]string, 0, len(m.config.Zones))
	for zoneName := range m.config.Zones {
		zoneNames = append(zoneNames, zoneName)
	}
	log.Printf("Finished processing zones: %s", strings.Join(zoneNames, ", "))

	return true
}

func main() {
	// Configure logging
	if os.Getenv("DEBUG") == "true" {
		log.Println("Log level is set to DEBUG")
	} else {
		log.Println("Running in normal mode")
	}

	// Get output directory
	outputDir := os.Getenv("DOWNLOAD_DIRECTORY")
	if outputDir == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			log.Fatalf("Could not determine home directory: %v", err)
		}
		outputDir = filepath.Join(homeDir, "Desktop", "maps")
	}

	// Create output directory
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		log.Fatalf("Destination '%s' can't be created: %v", outputDir, err)
	}
	log.Printf("Store destination set at: %s", outputDir)

	// Create app and load config
	app := NewMeshtasticTileDownloader(outputDir)
	if err := app.LoadConfig("config.yaml"); err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Validate config
	if !app.ValidateConfig() {
		log.Fatal("Configuration is not valid.")
	}

	// Get API key from environment
	providerEnvVar := strings.ToUpper(app.TileProvider() + "_API_KEY")
	app.apiKey = os.Getenv(providerEnvVar)
	if app.apiKey == "" {
		app.apiKey = os.Getenv("API_KEY")
	}

	// Check if API key is required and present
	if app.apiKey == "" && app.TileProvider() != "cnig.es" {
		log.Printf("Neither API_KEY env var or PROVIDER_API_KEY (ex: %s) found", providerEnvVar)
		log.Println("If your provider doesn't need an API Key, set the env var with any content.")
		os.Exit(1)
	}

	// Run app
	if !app.Run() {
		log.Println("Program finished with errors.")
		os.Exit(1)
	}

	log.Println("Program finished successfully")
}
