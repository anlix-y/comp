package ffmpeg

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"comp/internal/execx"
	"comp/internal/store"
)

type Runner struct {
	Store  store.Store
	Logger *zap.SugaredLogger
}

func (r *Runner) ffprobeDurationSeconds(input string) (float64, error) {
	out, errStr, err := execx.Run("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", input)
	if err == nil {
		if v, perr := strconv.ParseFloat(strings.TrimSpace(out), 64); perr == nil && v > 0 {
			if r.Logger != nil {
				r.Logger.Debugf("ffprobe(format) duration=%.3fs for %s", v, input)
			}
			return v, nil
		}
	} else if r.Logger != nil {
		r.Logger.Debugf("ffprobe(format) failed: %v: %s", err, strings.TrimSpace(errStr))
	}
	out2, errStr2, err2 := execx.Run("ffprobe", "-v", "error", "-show_entries", "stream=duration", "-of", "csv=p=0", input)
	if err2 == nil {
		lines := strings.Split(strings.TrimSpace(out2), "\n")
		var maxDur float64
		for _, ln := range lines {
			if ln == "N/A" || ln == "" {
				continue
			}
			if v, perr := strconv.ParseFloat(strings.TrimSpace(ln), 64); perr == nil && v > maxDur {
				maxDur = v
			}
		}
		if maxDur > 0 {
			if r.Logger != nil {
				r.Logger.Debugf("ffprobe(stream) duration=%.3fs for %s", maxDur, input)
			}
			return maxDur, nil
		}
	} else if r.Logger != nil {
		r.Logger.Debugf("ffprobe(stream) failed: %v: %s", err2, strings.TrimSpace(errStr2))
	}
	out3, errStr3, err3 := execx.Run("ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=duration", "-of", "default=nw=1:nk=1", input)
	if err3 == nil {
		if v, perr := strconv.ParseFloat(strings.TrimSpace(out3), 64); perr == nil && v > 0 {
			if r.Logger != nil {
				r.Logger.Debugf("ffprobe(v:0) duration=%.3fs for %s", v, input)
			}
			return v, nil
		}
	} else if r.Logger != nil {
		r.Logger.Debugf("ffprobe(v:0) failed: %v: %s", err3, strings.TrimSpace(errStr3))
	}
	return 0, fmt.Errorf("ffprobe: cannot determine duration")
}

// VideoProps returns width, height, fps (fps may be 0).
func (r *Runner) VideoProps(input string) (int, int, float64, error) {
	out, errStr, err := execx.Run("ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=width,height,avg_frame_rate,r_frame_rate", "-of", "default=nw=1:nk=1", input)
	if err != nil {
		if r.Logger != nil {
			r.Logger.Debugf("ffprobe(props) failed: %v: %s", err, strings.TrimSpace(errStr))
		}
		return 0, 0, 0, err
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var w, h int
	var fps float64
	parseRatio := func(s string) float64 {
		s = strings.TrimSpace(s)
		if s == "" || s == "N/A" {
			return 0
		}
		if strings.Contains(s, "/") {
			parts := strings.SplitN(s, "/", 2)
			num, _ := strconv.ParseFloat(parts[0], 64)
			den, _ := strconv.ParseFloat(parts[1], 64)
			if den != 0 {
				return num / den
			}
			if den == 0 && num > 0 {
				return num
			}
			return 0
		}
		v, _ := strconv.ParseFloat(s, 64)
		return v
	}
	if len(lines) >= 1 {
		w, _ = strconv.Atoi(strings.TrimSpace(lines[0]))
	}
	if len(lines) >= 2 {
		h, _ = strconv.Atoi(strings.TrimSpace(lines[1]))
	}
	if len(lines) >= 3 {
		fps = parseRatio(lines[2])
	}
	if fps == 0 && len(lines) >= 4 {
		fps = parseRatio(lines[3])
	}
	if r.Logger != nil {
		r.Logger.Debugf("ffprobe props for %s -> %dx%d @ %.3ffps", input, w, h, fps)
	}
	if w <= 0 || h <= 0 {
		return 0, 0, fps, fmt.Errorf("ffprobe: no dimensions")
	}
	return w, h, fps, nil
}

func (r *Runner) runWithProgress(taskID string, baseArgs []string, input string) error {
	ctx := context.Background()
	dur, derr := r.ffprobeDurationSeconds(input)
	if derr != nil {
		if r.Logger != nil {
			r.Logger.Warnf("[%s] duration unknown: %v", taskID, derr)
		}
	}
	args := append([]string{"-y", "-progress", "pipe:1", "-nostats"}, baseArgs...)
	cmd := exec.Command("ffmpeg", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	reader := bufio.NewReader(stdout)
	go func() {
		for {
			line, err := reader.ReadString('\n')
			if line != "" {
				if strings.HasPrefix(line, "out_time_ms=") {
					v := strings.TrimSpace(strings.TrimPrefix(line, "out_time_ms="))
					if ms, err := strconv.ParseFloat(v, 64); err == nil {
						pct := 0
						if dur > 0 {
							pct = int((ms/1e6)/dur*100 + 0.5)
							if pct > 99 {
								pct = 99
							}
							if pct < 0 {
								pct = 0
							}
						} else {
							seconds := ms / 1e6
							pct = int(seconds / 120.0 * 90.0)
							if pct > 90 {
								pct = 90
							}
							if pct < 0 {
								pct = 0
							}
						}
						_ = r.Store.Set(ctx, &store.TaskStatus{ID: taskID, Status: "processing", Stage: "transcode", Percent: pct}, 30*time.Minute)
					}
				}
			}
			if err != nil {
				break
			}
		}
	}()
	go func() { io.Copy(io.Discard, stderr) }()
	err = cmd.Wait()
	if err == nil {
		_ = r.Store.Set(ctx, &store.TaskStatus{ID: taskID, Status: "processing", Stage: "transcode", Percent: 100}, 30*time.Minute)
	}
	return err
}

func (r *Runner) Compress(taskID, input, output string, crf, maxWidth, fps int) error {
	// Clamp to source
	srcW, _, srcFPS, _ := r.VideoProps(input)
	effWidth := maxWidth
	if maxWidth > 0 && srcW > 0 && maxWidth > srcW {
		effWidth = srcW
	}
	effFPS := fps
	if fps > 0 && srcFPS > 0 {
		srcInt := int(srcFPS + 0.0001)
		if effFPS > srcInt {
			effFPS = srcInt
		}
		if effFPS < 1 {
			effFPS = 1
		}
	}
	args := []string{"-i", input}
	if effWidth > 0 {
		args = append(args, "-vf", fmt.Sprintf("scale='min(%d,iw)':-2", effWidth))
	}
	if effFPS > 0 {
		args = append(args, "-r", strconv.Itoa(effFPS))
	}
	codec := "libx265"
	audioCodec := "aac"
	pixFmt := "yuv420p"
	ext := strings.ToLower(filepath.Ext(output))
	if ext == ".webm" {
		codec = "libvpx-vp9"
		audioCodec = "libopus"
	}
	args = append(args, "-c:v", codec, "-preset", "slow", "-crf", strconv.Itoa(crf))
	if ext != ".webm" {
		args = append(args, "-pix_fmt", pixFmt)
	}
	args = append(args, "-c:a", audioCodec, "-b:a", "96k", output)
	return r.runWithProgress(taskID, args, input)
}

func (r *Runner) GIF(taskID, input, output string, width, fps int) error {
	srcW, _, srcFPS, _ := r.VideoProps(input)
	effWidth := width
	if width > 0 && srcW > 0 && width > srcW {
		effWidth = srcW
	}
	effFPS := fps
	if fps > 0 && srcFPS > 0 {
		srcInt := int(srcFPS + 0.0001)
		if effFPS > srcInt {
			effFPS = srcInt
		}
		if effFPS < 1 {
			effFPS = 1
		}
	}
	scaleFilter := "scale=iw:-1:flags=lanczos"
	if effWidth > 0 {
		scaleFilter = fmt.Sprintf("scale='min(%d,iw)':-1:flags=lanczos", effWidth)
	}
	vf := scaleFilter
	if effFPS > 0 {
		vf = fmt.Sprintf("fps=%d,%s", effFPS, scaleFilter)
	}
	args := []string{"-i", input, "-vf", vf, output}
	return r.runWithProgress(taskID, args, input)
}

func (r *Runner) Audio(taskID, input, output, bitrate string) error {
	args := []string{"-i", input, "-vn", "-c:a", "libmp3lame", "-b:a", bitrate, output}
	return r.runWithProgress(taskID, args, input)
}

func (r *Runner) Image(taskID, input, output string, quality, maxWidth int) error {
	// Simple image re-encode via ffmpeg
	args := []string{"-i", input}
	if maxWidth > 0 {
		args = append(args, "-vf", fmt.Sprintf("scale='min(%d,iw)':-2", maxWidth))
	}
	ext := strings.ToLower(filepath.Ext(output))
	switch ext {
	case ".jpg", ".jpeg":
		args = append(args, "-q:v", strconv.Itoa(quality))
	case ".png":
		// map quality (1-100) to compression level (0-9), inverse
		lvl := 9 - (quality / 12)
		if lvl < 0 {
			lvl = 0
		}
		if lvl > 9 {
			lvl = 9
		}
		args = append(args, "-compression_level", strconv.Itoa(lvl))
	}
	args = append(args, output)
	return r.runWithProgress(taskID, args, input)
}
