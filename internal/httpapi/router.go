package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	redis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	cfgpkg "comp/internal/config"
	"comp/internal/media/ffmpeg"
	"comp/internal/media/yt"
	"comp/internal/store"
)

type Deps struct {
	Cfg    cfgpkg.Config
	Logger *zap.SugaredLogger
	Store  store.Store
	Redis  *redis.Client
}

func NewRouter(d Deps) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.Default()

	templatesPath := chooseFirstExisting([]string{"web/templates/*", "templates/*"})
	if templatesPath != "" {
		r.LoadHTMLGlob(templatesPath)
	}
	staticPath := chooseFirstExisting([]string{"web/static", "static"})
	if staticPath != "" {
		r.Static("/static", staticPath)
	}
	uploadsPath := chooseFirstExisting([]string{"web/uploads", "uploads"})
	if uploadsPath == "" {
		uploadsPath = "uploads"
	}
	_ = os.MkdirAll(uploadsPath, 0o755)
	r.Static("/uploads", uploadsPath)

	r.GET("/", func(c *gin.Context) {
		c.HTML(http.StatusOK, "index.html", gin.H{})
	})

	r.GET("/status/:id", func(c *gin.Context) {
		id := c.Param("id")
		t, ok := d.Store.Get(context.Background(), id)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.JSON(http.StatusOK, t)
	})

	// Metadata endpoint for URLs: returns basic info using yt-dlp without downloading
	r.GET("/info", func(c *gin.Context) {
		url := strings.TrimSpace(c.Query("url"))
		if url == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "url is required"})
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		args := []string{"--dump-single-json", "--no-playlist", "--skip-download", url}
		if strings.TrimSpace(d.Cfg.Proxy) != "" {
			args = append([]string{"--proxy", d.Cfg.Proxy}, args...)
		}
		if d.Logger != nil {
			d.Logger.Debugf("/info yt-dlp %v", args)
		}
		cmd := exec.CommandContext(ctx, "yt-dlp", args...)
		out, err := cmd.Output()
		if err != nil {
			if d.Logger != nil {
				d.Logger.Warnf("yt-dlp info failed: %v", err)
			}
			c.JSON(http.StatusBadGateway, gin.H{"error": "failed to fetch info"})
			return
		}
		// yt-dlp --dump-single-json returns either a single object or a wrapper with 'entries'
		// We'll try to unmarshal into a generic map and normalize
		var raw map[string]any
		if err := json.Unmarshal(out, &raw); err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "invalid info format"})
			return
		}
		// pick first entry if playlist wrapper
		if entries, ok := raw["entries"].([]any); ok && len(entries) > 0 {
			if first, ok := entries[0].(map[string]any); ok {
				raw = first
			}
		}
		pickNum := func(m map[string]any, keys ...string) float64 {
			for _, k := range keys {
				if v, ok := m[k]; ok {
					switch t := v.(type) {
					case float64:
						return t
					case int:
						return float64(t)
					case int64:
						return float64(t)
					}
				}
			}
			return 0
		}
		title, _ := raw["title"].(string)
		ext, _ := raw["ext"].(string)
		format, _ := raw["format"].(string)
		if format == "" {
			format, _ = raw["format_id"].(string)
		}
		filesize := pickNum(raw, "filesize", "filesize_approx")
		duration := pickNum(raw, "duration")
		width := pickNum(raw, "width")
		height := pickNum(raw, "height")
		fps := pickNum(raw, "fps")
		// Bitrate may be in tbr (total bitrate)
		bitrate := pickNum(raw, "tbr")
		c.JSON(http.StatusOK, gin.H{
			"title":    title,
			"ext":      ext,
			"format":   format,
			"filesize": filesize,
			"duration": duration,
			"width":    width,
			"height":   height,
			"fps":      fps,
			"bitrate":  bitrate,
		})
	})

	r.POST("/upload", func(c *gin.Context) {
		pType := c.PostForm("type")
		crf, _ := strconv.Atoi(c.PostForm("crf"))
		width, _ := strconv.Atoi(c.PostForm("width"))
		fps, _ := strconv.Atoi(c.PostForm("fps"))
		quality, _ := strconv.Atoi(c.PostForm("quality"))
		imgFormat := strings.ToLower(strings.TrimSpace(c.PostForm("img_format")))
		url := strings.TrimSpace(c.PostForm("url"))

		var filename string
		var srcPath string

		if url == "" {
			file, err := c.FormFile("file")
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "File or URL is required"})
				return
			}
			filename = file.Filename
			// temporarily save to uploads before moving to tmp job dir
			srcPath = filepath.Join(uploadsPath, filename)
			if err := c.SaveUploadedFile(file, srcPath); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save file"})
				return
			}
		}

		taskID := uuid.New().String()
		_ = d.Store.Set(context.Background(), &store.TaskStatus{ID: taskID, Status: "processing", Stage: "init", Percent: 0}, 30*time.Minute)

		go func(taskID, pType, url, srcPath, filename, imgFormat string, crf, width, fps, quality int) {
			ctx := context.Background()
			runner := ffmpeg.Runner{Store: d.Store, Logger: d.Logger}
			jobDir := filepath.Join(os.TempDir(), "app", taskID)
			_ = os.MkdirAll(jobDir, 0o755)
			curPath := srcPath
			curName := filename
			// Download if URL provided
			if url != "" {
				f, err := yt.DownloadWithProgress(ctx, d.Store, d.Logger, taskID, url, d.Cfg.Proxy, jobDir)
				if err != nil {
					_ = d.Store.Set(ctx, &store.TaskStatus{ID: taskID, Status: "failed", Error: "download failed: " + err.Error()}, 30*time.Minute)
					return
				}
				curPath = f
				curName = filepath.Base(f)
			}
			// If local file: move into tmpfs job dir
			if url == "" && curPath != "" {
				dst := filepath.Join(jobDir, filepath.Base(curPath))
				_ = os.Rename(curPath, dst)
				curPath = dst
				curName = filepath.Base(dst)
			}

			ext := filepath.Ext(curName)
			outName := "out_" + curName
			outPath := filepath.Join(jobDir, outName)
			var errProc error
			switch pType {
			case "video_compress":
				outName = "compressed_" + curName
				outPath = filepath.Join(jobDir, outName)
				_ = d.Store.Set(ctx, &store.TaskStatus{ID: taskID, Status: "processing", Stage: "transcode", Percent: 0}, 30*time.Minute)
				errProc = runner.Compress(taskID, curPath, outPath, crf, width, fps)
			case "video_to_gif":
				outName = strings.TrimSuffix(curName, ext) + ".gif"
				outPath = filepath.Join(jobDir, outName)
				_ = d.Store.Set(ctx, &store.TaskStatus{ID: taskID, Status: "processing", Stage: "transcode", Percent: 0}, 30*time.Minute)
				errProc = runner.GIF(taskID, curPath, outPath, width, fps)
			case "video_to_audio":
				outName = strings.TrimSuffix(curName, ext) + ".mp3"
				outPath = filepath.Join(jobDir, outName)
				_ = d.Store.Set(ctx, &store.TaskStatus{ID: taskID, Status: "processing", Stage: "transcode", Percent: 0}, 30*time.Minute)
				errProc = runner.Audio(taskID, curPath, outPath, "128k")
			case "image_compress":
				// choose target image format if provided
				targetExt := ""
				switch imgFormat {
				case "jpg", "jpeg":
					targetExt = ".jpg"
				case "png":
					targetExt = ".png"
				}
				if targetExt == "" {
					// keep original extension
					outName = "compressed_" + curName
				} else {
					outName = "compressed_" + strings.TrimSuffix(curName, ext) + targetExt
				}
				outPath = filepath.Join(jobDir, outName)
				_ = d.Store.Set(ctx, &store.TaskStatus{ID: taskID, Status: "processing", Stage: "transcode", Percent: 0}, 30*time.Minute)
				errProc = runner.Image(taskID, curPath, outPath, quality, width)
			}
			if errProc != nil {
				_ = d.Store.Set(ctx, &store.TaskStatus{ID: taskID, Status: "failed", Error: "processing failed: " + errProc.Error()}, 30*time.Minute)
				return
			}
			// Move final file to uploads
			final := filepath.Join(uploadsPath, outName)
			_ = os.Rename(outPath, final)
			_ = d.Store.Set(ctx, &store.TaskStatus{ID: taskID, Status: "completed", OutputFile: outName, Stage: "finalize", Percent: 100}, 30*time.Minute)
			_ = os.RemoveAll(jobDir)
		}(taskID, pType, url, srcPath, filename, imgFormat, crf, width, fps, quality)

		c.JSON(http.StatusOK, gin.H{"task_id": taskID})
	})

	if d.Logger != nil {
		d.Logger.Infof("Server started on http://0.0.0.0:%d", d.Cfg.Port)
	} else {
		fmt.Printf("Server started on http://0.0.0.0:%d\n", d.Cfg.Port)
	}
	return r
}

func chooseFirstExisting(options []string) string {
	for _, p := range options {
		// For globs, check dir existence
		if strings.HasSuffix(p, "/*") {
			dir := strings.TrimSuffix(p, "/*")
			if st, err := os.Stat(dir); err == nil && st.IsDir() {
				return p
			}
		} else {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	return ""
}
