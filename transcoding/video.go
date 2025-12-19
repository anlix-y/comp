package transcoding

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func transcode(input, output string, args ...string) error {
	base := []string{"-y", "-i", input}
	base = append(base, args...)
	base = append(base, output)

	cmd := exec.Command("ffmpeg", base...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func videoToVideo(input, output string, codec string, crf int) error {
	return transcode(
		input,
		output,
		"-c:v", codec,
		"-crf", itoa(crf),
		"-preset", "medium",
		"-c:a", "aac",
		"-b:a", "128k",
	)
}

func VideoToGif(
	input string,
	output string,
	width int,
	fps int,
) error {

	palette := "palette.png"

	// Шаг 1 — палитра
	cmdPal := exec.Command(
		"ffmpeg",
		"-y",
		"-i", input,
		"-vf", "fps="+itoa(fps)+",scale="+itoa(width)+":-1:flags=lanczos,palettegen",
		palette,
	)
	cmdPal.Stdout = os.Stdout
	cmdPal.Stderr = os.Stderr
	if err := cmdPal.Run(); err != nil {
		return err
	}

	// Шаг 2 — GIF
	cmd := exec.Command(
		"ffmpeg",
		"-y",
		"-i", input,
		"-i", palette,
		"-lavfi",
		"fps="+itoa(fps)+",scale="+itoa(width)+":-1:flags=lanczos[x];[x][1:v]paletteuse",
		output,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

func VideoToAudio(input, output string, bitrate string) error {
	return transcode(
		input,
		output,
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
		output,
	)

	cmd := exec.Command("ffmpeg", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
