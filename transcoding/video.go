package transcoding

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func transcode(input, output string, args ...string) error {
	return TranscodeWithProgress(input, output, nil, args...)
}

func TranscodeWithProgress(input, output string, progressChan chan<- float64, args ...string) error {
	base := []string{"-y", "-i", input}
	base = append(base, args...)
	base = append(base, output)

	cmd := exec.Command("ffmpeg", base...)

	if progressChan != nil {
		stderr, err := cmd.StderrPipe()
		if err != nil {
			return err
		}
		if err := cmd.Start(); err != nil {
			return err
		}

		// Парсим длительность и прогресс
		go func() {
			defer close(progressChan)
			var duration float64
			scanner := bufio.NewScanner(stderr)
			scanner.Split(bufio.ScanWords)
			for scanner.Scan() {
				line := scanner.Text()
				if strings.Contains(line, "Duration:") {
					if scanner.Scan() {
						durationStr := strings.Trim(scanner.Text(), ",")
						duration = parseDuration(durationStr)
					}
				}
				if strings.HasPrefix(line, "time=") {
					timeStr := strings.TrimPrefix(line, "time=")
					currentTime := parseDuration(timeStr)
					if duration > 0 {
						progress := (currentTime / duration) * 100
						if progress > 100 {
							progress = 100
						}
						progressChan <- progress
					}
				}
			}
		}()

		return cmd.Wait()
	}

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func parseDuration(timeStr string) float64 {
	// HH:MM:SS.MS
	parts := strings.Split(timeStr, ":")
	if len(parts) != 3 {
		return 0
	}
	h, _ := strconv.ParseFloat(parts[0], 64)
	m, _ := strconv.ParseFloat(parts[1], 64)
	s, _ := strconv.ParseFloat(parts[2], 64)
	return h*3600 + m*60 + s
}

func VideoToGif(
	input string,
	output string,
	width int,
	fps int,
) error {
	return VideoToGifWithProgress(input, output, width, fps, nil)
}

func VideoToGifWithProgress(
	input string,
	output string,
	width int,
	fps int,
	progressChan chan<- float64,
) error {

	palette := filepath.Join(filepath.Dir(output), "palette.png")

	// Шаг 1 — палитра
	cmdPal := exec.Command(
		"ffmpeg",
		"-y",
		"-i", input,
		"-vf", "fps="+strconv.Itoa(fps)+",scale="+strconv.Itoa(width)+":-1:flags=lanczos,palettegen",
		palette,
	)
	cmdPal.Stdout = os.Stdout
	cmdPal.Stderr = os.Stderr
	if err := cmdPal.Run(); err != nil {
		return err
	}

	// Шаг 2 — GIF
	return TranscodeWithProgress(input, output, progressChan,
		"-i", palette,
		"-lavfi",
		"fps="+strconv.Itoa(fps)+",scale="+strconv.Itoa(width)+":-1:flags=lanczos[x];[x][1:v]paletteuse",
	)
}

func VideoToAudio(input, output string, bitrate string) error {
	return VideoToAudioWithProgress(input, output, bitrate, nil)
}

func VideoToAudioWithProgress(input, output string, bitrate string, progressChan chan<- float64) error {
	return TranscodeWithProgress(
		input,
		output,
		progressChan,
		"-vn",
		"-c:a", "libmp3lame",
		"-b:a", bitrate,
	)
}

func compressVideo(input, crf, output string) error {
	cmd := exec.Command(
		"ffmpeg",
		"-y",
		"-i", input,
		"-vcodec", "libx264",
		"-crf", crf, // качество: 18–23 хорошее, 28 сильное сжатие
		"-preset", "fast", // скорость
		"-acodec", "aac",
		"-b:a", "128k",
		output,
	)

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

func SuperCompressVideo(
	input string,
	output string,
	crf int, // 28–35
	maxWidth int, // 1920 / 1280 / 854
	fps int, // 30 / 24 / 0 = оставить
	progressChan chan<- float64,
) error {

	args := []string{"-y", "-i", input}

	// Ресайз
	if maxWidth > 0 {
		args = append(args,
			"-vf", fmt.Sprintf("scale='min(%d,iw)':-2", maxWidth),
		)
	}

	// FPS
	if fps > 0 {
		args = append(args, "-r", strconv.Itoa(fps))
	}

	// Видео
	codec := "libx265"
	audioCodec := "aac"
	pixFmt := "yuv420p"

	ext := strings.ToLower(filepath.Ext(output))
	if ext == ".webm" {
		codec = "libvpx-vp9"
		audioCodec = "libopus"
	}

	args = append(args,
		"-c:v", codec,
		"-preset", "slow", // лучшее сжатие
		"-crf", strconv.Itoa(crf),
	)

	if ext != ".webm" {
		args = append(args, "-pix_fmt", pixFmt)
	}

	// Аудио
	args = append(args,
		"-c:a", audioCodec,
		"-b:a", "96k",
		// output добавим в TranscodeWithProgress
	)

	return TranscodeWithProgress(input, output, progressChan, args[3:]...)
}
