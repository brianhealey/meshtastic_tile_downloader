package main

import (
	"bytes"
	"flag"
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
	"time"

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

// Point represents a point on the map
type Point struct {
	Lat  float64
	Long float64
}

// MeshtasticTileDownloader is the main application struct
type MeshtasticTileDownloader struct {
	config          Config
	outputDirectory string
	apiKey          string
	isPointRadius   bool
	centerPoint     Point
	radiusKm        float64
	detailLevel     int
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

	// When using point-radius mode, we don't need to validate zones
	if m.isPointRadius {
		log.Printf("Using point-radius mode: center (%f, %f), radius %f km, detail level %d",
			m.centerPoint.Lat, m.centerPoint.Long, m.radiusKm, m.detailLevel)
	} else {
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

// TileXToLong converts tile X coordinate to longitude
func (m *MeshtasticTileDownloader) TileXToLong(x int, zoom int) float64 {
	xyTilesCount := math.Pow(2, float64(zoom))
	return (float64(x) / xyTilesCount * 360.0) - 180.0
}

// TileYToLat converts tile Y coordinate to latitude
func (m *MeshtasticTileDownloader) TileYToLat(y int, zoom int) float64 {
	xyTilesCount := math.Pow(2, float64(zoom))
	n := math.Pi - 2.0*math.Pi*float64(y)/xyTilesCount
	return 180.0 / math.Pi * math.Atan(0.5*(math.Exp(n)-math.Exp(-n)))
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
	defer resp.Body.Close()

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
	defer f.Close()

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
	defer f.Close()

	// Use png encoder with best compression
	encoder := png.Encoder{
		CompressionLevel: png.BestCompression,
	}
	return encoder.Encode(f, img)
}

// CalculatePointRadiusBounds calculates the bounding box around a point using the Haversine formula
func (m *MeshtasticTileDownloader) CalculatePointRadiusBounds() (minLat, minLon, maxLat, maxLon float64) {
	// Earth's radius in kilometers
	//earthRadius := 6371.0

	// Convert radius from km to degrees (approximate)
	// 1 degree of latitude is approximately 111.32 km at the equator
	// 1 degree of longitude varies with latitude
	latRadius := m.radiusKm / 111.32

	// Longitude degrees per km varies with latitude
	// cos(lat) gives the scale factor
	longRadius := m.radiusKm / (111.32 * math.Cos(m.centerPoint.Lat*math.Pi/180.0))

	minLat = m.centerPoint.Lat - latRadius
	maxLat = m.centerPoint.Lat + latRadius
	minLon = m.centerPoint.Long - longRadius
	maxLon = m.centerPoint.Long + longRadius

	// Handle latitude boundary conditions
	if minLat < -90.0 {
		minLat = -90.0
	}
	if maxLat > 90.0 {
		maxLat = 90.0
	}

	// Handle longitude wrap-around
	if minLon < -180.0 {
		minLon += 360.0
	}
	if maxLon > 180.0 {
		maxLon -= 360.0
	}

	return minLat, minLon, maxLat, maxLon
}

// GetZoomLevelsForDetail returns the appropriate zoom level range based on detail level
func (m *MeshtasticTileDownloader) GetZoomLevelsForDetail() []int {
	var minZoom, maxZoom int

	// Map detail level to zoom levels
	switch m.detailLevel {
	case 1: // Low detail - good for very large areas
		minZoom = 6
		maxZoom = 10
	case 2: // Medium detail - balanced for regional areas
		minZoom = 7
		maxZoom = 12
	case 3: // High detail - good for cities and towns
		minZoom = 8
		maxZoom = 14
	case 4: // Very high detail - for detailed city navigation
		minZoom = 9
		maxZoom = 16
	default: // Default to medium detail
		minZoom = 7
		maxZoom = 12
	}

	// Create zoom level range
	zoomLevels := make([]int, 0, maxZoom-minZoom+1)
	for i := minZoom; i <= maxZoom; i++ {
		zoomLevels = append(zoomLevels, i)
	}

	return zoomLevels
}

// estimateTileSize estimates the average size of a tile at a specific zoom level
func (m *MeshtasticTileDownloader) estimateTileSize(zoom int) int64 {
	// These are rough estimates based on average tile sizes
	// Size generally increases with zoom level as tiles contain more detail
	switch {
	case zoom <= 5:
		return 20 * 1024 // ~20KB for very low zoom levels
	case zoom <= 8:
		return 30 * 1024 // ~30KB for low zoom levels
	case zoom <= 11:
		return 50 * 1024 // ~50KB for medium zoom levels
	case zoom <= 14:
		return 80 * 1024 // ~80KB for high zoom levels
	default:
		return 120 * 1024 // ~120KB for very high zoom levels
	}
}

// formatSize formats a byte size to a human-readable string (KB, MB, GB)
func formatSize(bytes int64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
	)

	switch {
	case bytes >= GB:
		return fmt.Sprintf("%.2f GB", float64(bytes)/float64(GB))
	case bytes >= MB:
		return fmt.Sprintf("%.2f MB", float64(bytes)/float64(MB))
	case bytes >= KB:
		return fmt.Sprintf("%.2f KB", float64(bytes)/float64(KB))
	default:
		return fmt.Sprintf("%d bytes", bytes)
	}
}

// ObtainTiles downloads all tiles for the given regions and zoom levels
func (m *MeshtasticTileDownloader) ObtainTiles(regions []string, zoomLevels []int) error {
	totalTiles := 0
	estimatedSize := int64(0)
	tileCountByZoom := make(map[int]int)

	// Calculate total tiles first for progress bar and size estimation
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
			tilesInZoom := tilesX * tilesY

			tileCountByZoom[zoom] += tilesInZoom
			totalTiles += tilesInZoom
		}
	}

	// Calculate estimated size
	for zoom, count := range tileCountByZoom {
		zoomSize := int64(count) * m.estimateTileSize(zoom)
		estimatedSize += zoomSize
		log.Printf("Zoom level %d: %d tiles, estimated %s", zoom, count, formatSize(zoomSize))
	}

	log.Printf("Total tiles: %d, Estimated download size: %s", totalTiles, formatSize(estimatedSize))

	// Ask for confirmation if size is large
	if estimatedSize > 100*1024*1024 { // 100MB
		fmt.Printf("\nWarning: The estimated download size is %s. Continue? (y/n): ", formatSize(estimatedSize))
		var answer string
		fmt.Scanln(&answer)
		if strings.ToLower(answer) != "y" && strings.ToLower(answer) != "yes" {
			return fmt.Errorf("download cancelled by user")
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

// RunPointRadius executes the tile download process for point-radius mode
func (m *MeshtasticTileDownloader) RunPointRadius() bool {
	startTime := time.Now()

	// Calculate bounding box from point and radius
	minLat, minLon, maxLat, maxLon := m.CalculatePointRadiusBounds()
	regionStr := fmt.Sprintf("%.6f,%.6f,%.6f,%.6f", minLat, minLon, maxLat, maxLon)
	regions := []string{regionStr}

	log.Printf("Point-radius mode: center (%.6f, %.6f), radius %.2f km",
		m.centerPoint.Lat, m.centerPoint.Long, m.radiusKm)
	log.Printf("Bounding box: %.6f,%.6f,%.6f,%.6f", minLat, minLon, maxLat, maxLon)

	// Get zoom levels based on detail level
	zoomLevels := m.GetZoomLevelsForDetail()
	log.Printf("Detail level %d maps to zoom levels %v", m.detailLevel, zoomLevels)

	folderName := fmt.Sprintf("point_%.4f_%.4f_r%.1f_d%d",
		m.centerPoint.Lat, m.centerPoint.Long, m.radiusKm, m.detailLevel)

	// Create a dedicated output directory for this point
	pointOutputDir := filepath.Join(m.outputDirectory, folderName)
	if err := os.MkdirAll(pointOutputDir, 0755); err != nil {
		log.Printf("Error creating output directory for point: %v", err)
		return false
	}

	// Save the bounds to a metadata file
	metadataPath := filepath.Join(pointOutputDir, "metadata.txt")
	metadataContent := fmt.Sprintf("Center: %.6f, %.6f\nRadius: %.2f km\nDetail Level: %d\nZoom Levels: %v\nBounding Box: %.6f,%.6f,%.6f,%.6f\nTimestamp: %s\n",
		m.centerPoint.Lat, m.centerPoint.Long, m.radiusKm, m.detailLevel,
		zoomLevels, minLat, minLon, maxLat, maxLon, time.Now().Format(time.RFC3339))

	if err := os.WriteFile(metadataPath, []byte(metadataContent), 0644); err != nil {
		log.Printf("Error writing metadata: %v", err)
	}

	// Store original output directory
	originalOutputDir := m.outputDirectory
	// Set output directory to the point-specific directory
	m.outputDirectory = pointOutputDir

	if err := m.ObtainTiles(regions, zoomLevels); err != nil {
		if err.Error() == "download cancelled by user" {
			log.Println("Download cancelled by user")
			// Restore original output directory
			m.outputDirectory = originalOutputDir
			return true // User cancellation is not an error
		}
		log.Printf("Error obtaining tiles: %v", err)
		// Restore original output directory
		m.outputDirectory = originalOutputDir
		return false
	}

	// Restore original output directory
	m.outputDirectory = originalOutputDir

	elapsedTime := time.Since(startTime)
	log.Printf("Total download time: %s", elapsedTime.Round(time.Second))

	return true
}

// Run executes the tile download process for all configured zones
func (m *MeshtasticTileDownloader) Run() bool {
	if !m.IsValidProvider() {
		log.Printf("Unknown provider '%s'", m.TileProvider())
		return false
	}

	// If in point-radius mode, use that instead of the configuration file
	if m.isPointRadius {
		return m.RunPointRadius()
	}

	startTime := time.Now()

	for zoneName, zone := range m.config.Zones {
		// Create zoom level range
		zoomLevels := make([]int, 0, zone.Zoom.In-zone.Zoom.Out+1)
		for i := zone.Zoom.Out; i <= zone.Zoom.In; i++ {
			zoomLevels = append(zoomLevels, i)
		}

		log.Printf("Obtaining zone [%s] [zoom: %d → %d] regions: %v",
			zoneName, zone.Zoom.Out, zone.Zoom.In, zone.Regions)

		if err := m.ObtainTiles(zone.Regions, zoomLevels); err != nil {
			if err.Error() == "download cancelled by user" {
				log.Printf("Download cancelled by user for zone %s", zoneName)
				return true // User cancellation is not an error
			}
			log.Printf("Error obtaining tiles for zone %s: %v", zoneName, err)
			return false
		}

		log.Printf("Finished with zone %s", zoneName)
	}

	elapsedTime := time.Since(startTime)
	log.Printf("Total download time: %s", elapsedTime.Round(time.Second))

	// List all processed zones
	zoneNames := make([]string, 0, len(m.config.Zones))
	for zoneName := range m.config.Zones {
		zoneNames = append(zoneNames, zoneName)
	}
	log.Printf("Finished processing zones: %s", strings.Join(zoneNames, ", "))

	return true
}

func main() {
	// Parse command-line arguments for point-radius mode
	var lat, long, radius float64
	var detailLevel int
	var usePointMode bool

	flag.Float64Var(&lat, "lat", 0, "Center latitude for point-radius mode")
	flag.Float64Var(&long, "long", 0, "Center longitude for point-radius mode")
	flag.Float64Var(&radius, "radius", 0, "Radius in kilometers for point-radius mode")
	flag.IntVar(&detailLevel, "detail", 2, "Detail level (1-4) for point-radius mode")
	flag.BoolVar(&usePointMode, "point", false, "Enable point-radius mode")
	flag.Parse()

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

	// Create app
	app := NewMeshtasticTileDownloader(outputDir)

	// Check if we're using point-radius mode
	if usePointMode {
		if lat == 0 && long == 0 {
			log.Fatal("Error: When using point mode, you must specify lat and long parameters")
		}

		if radius <= 0 {
			log.Fatal("Error: Radius must be greater than 0")
		}

		if detailLevel < 1 || detailLevel > 4 {
			log.Printf("Warning: Detail level %d is out of range (1-4), using default level 2", detailLevel)
			detailLevel = 2
		}

		// Set point-radius mode parameters
		app.isPointRadius = true
		app.centerPoint = Point{Lat: lat, Long: long}
		app.radiusKm = radius
		app.detailLevel = detailLevel

		// Still need to load config for map provider settings
		if err := app.LoadConfig("config.yaml"); err != nil {
			log.Printf("Warning: Failed to load configuration: %v. Using defaults.", err)
			// Set some sensible defaults
			app.config.Map.Provider = "thunderforest"
			app.config.Map.Style = "atlas"
			app.config.Map.Reduce = 12
		}
	} else {
		// Regular mode - load config
		if err := app.LoadConfig("config.yaml"); err != nil {
			log.Fatalf("Failed to load configuration: %v", err)
		}
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
