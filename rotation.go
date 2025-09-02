package main

import (
	"fmt"
	"log"
	"math/rand"
	"path/filepath"
	"sync/atomic"
)

type ImageRotator struct {
	images      []WeightedImageData
	totalWeight int
	current     int64
	mode        string
	connections int64
}

type WeightedImageData struct {
	fb     *fb
	weight int
	path   string
}

func NewImageRotator(config ServerConfig) (*ImageRotator, error) {
	rotator := &ImageRotator{
		mode: config.RotationMode,
	}

	// Load images with weights
	if len(config.Images) > 0 {
		// Use weighted image array
		for _, img := range config.Images {
			imagePath := filepath.Join(defaultImageDir, img.Path)
			fb, err := loadImage(imagePath)
			if err != nil {
				log.Printf("[WARN] Failed to load image %s: %v", imagePath, err)
				continue
			}

			weight := img.Weight
			if weight <= 0 {
				weight = 10 // default weight if not specified
			}

			rotator.images = append(rotator.images, WeightedImageData{
				fb:     fb,
				weight: weight,
				path:   img.Path,
			})
			rotator.totalWeight += weight
		}
	} else if config.Image != "" {
		// Fallback to single image
		imagePath := filepath.Join(defaultImageDir, config.Image)
		fb, err := loadImage(imagePath)
		if err != nil {
			return nil, fmt.Errorf("failed to load image %s: %v", imagePath, err)
		}

		rotator.images = append(rotator.images, WeightedImageData{
			fb:     fb,
			weight: 1,
			path:   config.Image,
		})
		rotator.totalWeight = 1
	} else {
		return nil, fmt.Errorf("no images specified in config")
	}

	if len(rotator.images) == 0 {
		return nil, fmt.Errorf("no valid images loaded")
	}

	log.Printf("[INFO] Loaded %d images for rotation (mode: %s)", len(rotator.images), rotator.mode)
	return rotator, nil
}

func (r *ImageRotator) GetImage() *fb {
	if len(r.images) == 1 {
		return r.images[0].fb
	}

	switch r.mode {
	case "random":
		return r.getRandomWeighted()
	case "sequential":
		return r.getSequential()
	default:
		// Default to random weighted
		return r.getRandomWeighted()
	}
}

func (r *ImageRotator) getRandomWeighted() *fb {
	if r.totalWeight == 0 {
		return r.images[0].fb
	}

	// Generate random number from 1 to totalWeight
	target := rand.Intn(r.totalWeight) + 1
	current := 0

	for _, img := range r.images {
		current += img.weight
		if target <= current {
			return img.fb
		}
	}

	// Fallback (should not happen)
	return r.images[0].fb
}

func (r *ImageRotator) getSequential() *fb {
	idx := atomic.LoadInt64(&r.current) % int64(len(r.images))
	atomic.AddInt64(&r.current, 1)
	return r.images[idx].fb
}

func (r *ImageRotator) GetImageForConnection() *fb {
	atomic.AddInt64(&r.connections, 1)
	return r.GetImage()
}

func (r *ImageRotator) GetStats() map[string]interface{} {
	stats := make(map[string]interface{})
	stats["mode"] = r.mode
	stats["total_images"] = len(r.images)
	stats["total_connections"] = atomic.LoadInt64(&r.connections)
	stats["current_index"] = atomic.LoadInt64(&r.current)

	imageStats := make([]map[string]interface{}, len(r.images))
	for i, img := range r.images {
		imageStats[i] = map[string]interface{}{
			"path":   img.path,
			"weight": img.weight,
		}
	}
	stats["images"] = imageStats

	return stats
}
