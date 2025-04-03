package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func (cfg apiConfig) ensureAssetsDir() error {
	if _, err := os.Stat(cfg.assetsRoot); os.IsNotExist(err) {
		return os.Mkdir(cfg.assetsRoot, 0755)
	}
	return nil
}

func getAssetPath(mediaType string) string {
	key := make([]byte, 32)
	_, err := rand.Read(key)
	if err != nil {
		panic("failed to generate random bytes")
	}
	filename := base64.RawURLEncoding.EncodeToString(key)

	ext := mediaTypeToExtension(mediaType)
	return fmt.Sprintf("%s%s", filename, ext)
}

func (cfg apiConfig) getAssetDiskPath(filename string) string {
	return filepath.Join(cfg.assetsRoot, filename)
}

func (cfg apiConfig) getAssetURL(filename string) string {
	return fmt.Sprintf("http://localhost:%s/assets/%s", cfg.port, filename)
}

func (cfg apiConfig) getObjectURL(key string) string {
	return fmt.Sprintf("https://%s/%s", cfg.s3CfDistribution, key)
}

func mediaTypeToExtension(mediaType string) string {
	parts := strings.Split(mediaType, "/")
	if len(parts) != 2 {
		return ".bin"
	}
	return "." + parts[1]
}
