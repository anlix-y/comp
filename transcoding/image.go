package transcoding

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func compressImage(input, output string, quality int) error {
	ext := strings.ToLower(filepath.Ext(output))

	var args []string

	switch ext {
	case ".jpg", ".jpeg":
		args = []string{"-y", "-i", input, "-q:v", itoa(quality), output}
	case ".png":
		args = []string{"-y", "-i", input, "-compression_level", "9", output}
	case ".webp":
		args = []string{"-y", "-i", input, "-quality", itoa(quality), output}
	default:
		return nil
	}

	cmd := exec.Command("ffmpeg", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func CompressImageResize(
	input string,
	output string,
	quality int,
	maxWidth int, // 0 = без ограничения
	maxHeight int, // 0 = без ограничения
) error {

	ext := strings.ToLower(filepath.Ext(output))

	// scale фильтр
	scale := "scale=iw:ih"
	if maxWidth > 0 && maxHeight > 0 {
		scale = fmt.Sprintf("scale='min(%d,iw)':min(%d,ih):force_original_aspect_ratio=decrease",
			maxWidth, maxHeight)
	} else if maxWidth > 0 {
		scale = fmt.Sprintf("scale='min(%d,iw)':-1", maxWidth)
	} else if maxHeight > 0 {
		scale = fmt.Sprintf("scale=-1:'min(%d,ih)'", maxHeight)
	}

	args := []string{"-y", "-i", input, "-vf", scale}

	switch ext {
	case ".jpg", ".jpeg":
		args = append(args,
			"-q:v", strconv.Itoa(quality),
			output,
		)

	case ".png":
		args = append(args,
			"-compression_level", "9",
			output,
		)

	case ".webp":
		args = append(args,
			"-quality", strconv.Itoa(quality),
			output,
		)

	default:
		return fmt.Errorf("неподдерживаемый формат: %s", ext)
	}

	cmd := exec.Command("ffmpeg", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func itoa(v int) string {
	return strings.TrimSpace(string([]byte{
		byte('0' + v/10),
		byte('0' + v%10),
	}))
}
