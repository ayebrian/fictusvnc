package main

import (
	"fmt"
	"log/slog"
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

// NewImageRotator loads a server's images. Relative image paths are resolved
// against baseDir (the config file's directory) rather than the process
// working directory, so the same config works from a shell and from systemd.
func NewImageRotator(config ServerConfig, baseDir string) (*ImageRotator, error) {
	rotator := &ImageRotator{
		mode: config.RotationMode,
	}

	resolve := func(p string) string {
		if filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(baseDir, defaultImageDir, p)
	}

	// Load images with weights
	if len(config.Images) > 0 {
		// Use weighted image array
		for _, img := range config.Images {
			imagePath := resolve(img.Path)
			fb, err := loadImage(imagePath)
			if err != nil {
				slog.Warn("failed to load image, skipping", "path", imagePath, "error", err)
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
		imagePath := resolve(config.Image)
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

	slog.Debug("loaded images", "count", len(rotator.images), "rotation_mode", rotator.modeName())
	return rotator, nil
}

// modeName reports the effective rotation mode, resolving the empty and
// unrecognised settings to the "random" default that GetImage actually uses.
func (r *ImageRotator) modeName() string {
	if r.mode == "sequential" {
		return "sequential"
	}
	return "random"
}

func (r *ImageRotator) GetImage() *fb { return r.pick().fb }

// pick selects the entry to serve next according to the rotation mode.
func (r *ImageRotator) pick() *WeightedImageData {
	if len(r.images) == 1 {
		return &r.images[0]
	}

	if r.mode == "sequential" {
		return r.getSequential()
	}
	// Everything else, including an empty or unrecognised mode, is weighted
	// random.
	return r.getRandomWeighted()
}

func (r *ImageRotator) getRandomWeighted() *WeightedImageData {
	if r.totalWeight == 0 {
		return &r.images[0]
	}

	// Generate random number from 1 to totalWeight
	target := rand.Intn(r.totalWeight) + 1
	current := 0

	for i := range r.images {
		current += r.images[i].weight
		if target <= current {
			return &r.images[i]
		}
	}

	// Fallback (should not happen)
	return &r.images[0]
}

func (r *ImageRotator) getSequential() *WeightedImageData {
	// Claim the slot and advance in one atomic step; a separate Load+Add
	// hands the same image to concurrent connections.
	idx := (atomic.AddInt64(&r.current, 1) - 1) % int64(len(r.images))
	return &r.images[idx]
}

// GetImageForConnection picks the image for a new connection and reports its
// configured path so the connection record can name what the client saw.
func (r *ImageRotator) GetImageForConnection() (*fb, string) {
	atomic.AddInt64(&r.connections, 1)
	e := r.pick()
	return e.fb, e.path
}
