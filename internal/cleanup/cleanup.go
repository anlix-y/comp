package cleanup

import (
	"os"
	"path/filepath"
	"time"
)

// Start periodically removes files older than keepMinutes from uploadsDir.
func Start(uploadsDir string, keepMinutes int) {
	if keepMinutes <= 0 {
		return
	}
	ticker := time.NewTicker(1 * time.Minute)
	go func() {
		for range ticker.C {
			entries, err := os.ReadDir(uploadsDir)
			if err != nil {
				continue
			}
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				fi, err := e.Info()
				if err != nil {
					continue
				}
				if time.Since(fi.ModTime()) > time.Duration(keepMinutes)*time.Minute {
					_ = os.Remove(filepath.Join(uploadsDir, e.Name()))
				}
			}
		}
	}()
}
