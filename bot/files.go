package bot

import (
	"fmt"
	"log"
	"os"
	"time"
)

func addTempFile(guildID, filename string) {
	tempFilesMu.Lock()
	defer tempFilesMu.Unlock()

	if tempFiles[guildID] == nil {
		tempFiles[guildID] = make([]string, 0)
	}
	tempFiles[guildID] = append(tempFiles[guildID], filename)
}

func removeTempFile(guildID, filename string) {
	tempFilesMu.Lock()
	defer tempFilesMu.Unlock()

	if files, exists := tempFiles[guildID]; exists {
		for i, file := range files {
			if file == filename {
				tempFiles[guildID] = append(files[:i], files[i+1:]...)
				break
			}
		}
		// Clean up empty slice
		if len(tempFiles[guildID]) == 0 {
			delete(tempFiles, guildID)
		}
	}

	// Remove the actual file
	os.Remove(filename)
}

func cleanupTempFiles(guildID string) {
	tempFilesMu.Lock()
	defer tempFilesMu.Unlock()

	if files, exists := tempFiles[guildID]; exists {
		for _, filename := range files {
			os.Remove(filename)
			log.Printf("Cleaned up temp file: %s", filename)
		}
		delete(tempFiles, guildID)
	}
}

func cleanupAllTempFiles() {
	tempFilesMu.Lock()
	defer tempFilesMu.Unlock()

	for _, files := range tempFiles {
		for _, filename := range files {
			os.Remove(filename)
			log.Printf("Cleaned up temp file: %s", filename)
		}
	}
	tempFiles = make(map[string][]string)
}

func waitForfileReady(filename string, maxWait time.Duration) error {
	start := time.Now()
	var lastSize int64 = -1
	stableCount := 0

	for time.Since(start) < maxWait {
		info, err := os.Stat(filename)
		if err != nil {
			if os.IsNotExist(err) {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			return fmt.Errorf("file stat error: %w", err)
		}

		currentSize := info.Size()

		// File must have content
		if currentSize == 0 {
			time.Sleep(100 * time.Millisecond)
			continue
		}

		// Check if file size is stable (not still being written to)
		if currentSize == lastSize {
			stableCount++
			// File size has been stable for at least 500ms
			if stableCount >= 5 {
				// Additional check - try to open file to ensure it's not locked
				file, err := os.OpenFile(filename, os.O_RDONLY, 0)
				if err != nil {
					time.Sleep(100 * time.Millisecond)
					continue
				}
				file.Close()

				log.Printf("File %s is ready (size: %d bytes, stable for %dms)",
					filename, currentSize, stableCount*100)
				return nil
			}
		} else {
			stableCount = 0
			lastSize = currentSize
		}

		time.Sleep(100 * time.Millisecond)
	}

	return fmt.Errorf("file %s not ready after %v (last size: %d)", filename, maxWait, lastSize)
}
