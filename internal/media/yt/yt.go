package yt

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"comp/internal/store"
)

func binaryPath() string {
	// Prefer env override, otherwise rely on PATH
	if p := os.Getenv("YT_DLP"); p != "" {
		return p
	}
	return "yt-dlp"
}

// DownloadWithProgress downloads URL into jobDir using yt-dlp and updates Store with download stage percent.
// Returns the downloaded file path.
func DownloadWithProgress(ctx context.Context, st store.Store, log *zap.SugaredLogger, taskID, url, proxy, jobDir string) (string, error) {
	bin := binaryPath()
	outputPattern := filepath.Join(jobDir, "%(title)s.%(ext)s")
	// First, resolve future file name
	argsName := []string{"--get-filename", "-o", outputPattern, "--restrict-filenames", url}
	if strings.TrimSpace(proxy) != "" {
		argsName = append([]string{"--proxy", proxy}, argsName...)
	}
	if log != nil {
		log.Infof("yt-dlp get-filename: %s %v", bin, argsName)
	}
	cmdName := exec.CommandContext(ctx, bin, argsName...)
	b, err := cmdName.Output()
	if err != nil {
		return "", err
	}
	filename := strings.TrimSpace(string(b))

	// Now download with progress
	args := []string{"-o", outputPattern, "--restrict-filenames", "--newline", url}
	if strings.TrimSpace(proxy) != "" {
		args = append([]string{"--proxy", proxy}, args...)
	}
	if log != nil {
		log.Infof("yt-dlp download: %s %v", bin, args)
	}
	_ = st.Set(ctx, &store.TaskStatus{ID: taskID, Status: "processing", Stage: "download", Percent: 0}, 30*time.Minute)
	cmd := exec.CommandContext(ctx, bin, args...)
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return "", err
	}
	re := regexp.MustCompile(`(?i)(\d{1,3}\.\d+)%`) // 12.3%
	go func() {
		r := bufio.NewScanner(stdout)
		for r.Scan() {
			line := r.Text()
			m := re.FindStringSubmatch(line)
			if len(m) >= 2 {
				if f, err := strconv.ParseFloat(m[1], 64); err == nil {
					pct := int(f + 0.5)
					if pct > 100 {
						pct = 100
					}
					if pct < 0 {
						pct = 0
					}
					_ = st.Set(ctx, &store.TaskStatus{ID: taskID, Status: "processing", Stage: "download", Percent: pct}, 30*time.Minute)
				}
			}
		}
	}()
	go func() { _ = bufio.NewScanner(stderr).Err() }()
	if err := cmd.Wait(); err != nil {
		return "", err
	}
	_ = st.Set(ctx, &store.TaskStatus{ID: taskID, Status: "processing", Stage: "download", Percent: 100}, 30*time.Minute)
	return filename, nil
}
