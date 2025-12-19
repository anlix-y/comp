package main

import (
	"comp/transcoding"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type Config struct {
	Proxy          string `json:"proxy"`
	CleanupMinutes int    `json:"cleanup_minutes"`
	Port           int    `json:"port"`
}

var config Config

func loadConfig() {
	file, err := os.Open("config.json")
	if err != nil {
		fmt.Printf("!!! Ошибка загрузки конфига: %v. Используем значения по умолчанию.\n", err)
		config = Config{Port: 3000, CleanupMinutes: 5}
		return
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	err = decoder.Decode(&config)
	if err != nil {
		fmt.Printf("!!! Ошибка парсинга конфига: %v\n", err)
		config = Config{Port: 3000, CleanupMinutes: 5}
	}
}

type TaskStatus struct {
	ID         string `json:"id"`
	Status     string `json:"status"` // "pending", "processing", "completed", "failed"
	OutputFile string `json:"output_file"`
	Error      string `json:"error"`
}

var (
	tasks   = make(map[string]*TaskStatus)
	tasksMu sync.Mutex
)

func getYTlpPath() string {
	ytDlpPath := "yt-dlp"
	localPath := filepath.Join(".", "yt-dlp", "yt-dlp")
	if _, err := os.Stat(localPath); err == nil {
		ytDlpPath = localPath
	} else {
		localPathExe := localPath + ".exe"
		if _, err := os.Stat(localPathExe); err == nil {
			ytDlpPath = localPathExe
		}
	}
	return ytDlpPath
}

func updateTaskError(id, errMsg string) {
	tasksMu.Lock()
	defer tasksMu.Unlock()
	if task, ok := tasks[id]; ok {
		task.Status = "failed"
		task.Error = errMsg
	}
}

func main() {
	loadConfig()
	cleanupRoutine()
	gin.SetMode(gin.ReleaseMode)
	r := gin.Default()

	// Статика
	r.Static("/static", "./static")
	r.Static("/uploads", "./uploads")

	// Главная страница
	r.GET("/", func(c *gin.Context) {
		c.HTML(http.StatusOK, "index.html", nil)
	})

	// Эндпоинт для получения инфо о видео
	r.POST("/info", func(c *gin.Context) {
		url := c.PostForm("url")
		if url == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "URL is required"})
			return
		}

		ytDlpPath := getYTlpPath()
		args := []string{"--dump-json", "--restrict-filenames", url}
		if config.Proxy != "" {
			args = append([]string{"--proxy", config.Proxy}, args...)
		}

		cmd := exec.Command(ytDlpPath, args...)
		out, err := cmd.Output()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get video info: " + err.Error()})
			return
		}

		var info map[string]interface{}
		if err := json.Unmarshal(out, &info); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse video info"})
			return
		}

		c.JSON(http.StatusOK, info)
	})

	// Эндпоинт для проверки статуса
	r.GET("/status/:id", func(c *gin.Context) {
		id := c.Param("id")
		tasksMu.Lock()
		task, ok := tasks[id]
		tasksMu.Unlock()

		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "Task not found"})
			return
		}
		c.JSON(http.StatusOK, task)
	})

	// Загрузка файла или обработка ссылки
	r.POST("/upload", func(c *gin.Context) {
		url := c.PostForm("url")
		processType := c.PostForm("type")
		crf, _ := strconv.Atoi(c.PostForm("crf"))
		width, _ := strconv.Atoi(c.PostForm("width"))
		fps, _ := strconv.Atoi(c.PostForm("fps"))
		quality, _ := strconv.Atoi(c.PostForm("quality"))

		var dst string
		var filename string

		if url == "" {
			file, err := c.FormFile("file")
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "File or URL is required"})
				return
			}
			filename = file.Filename
			dst = filepath.Join("uploads", filename)
			if err := c.SaveUploadedFile(file, dst); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save file"})
				return
			}
		}

		taskID := uuid.New().String()
		tasksMu.Lock()
		tasks[taskID] = &TaskStatus{ID: taskID, Status: "processing"}
		tasksMu.Unlock()

		go func(tID, u, pType, d, fName string, cVal, wVal, fVal, qVal int) {
			var errProcessing error
			currentDst := d
			currentFilename := fName

			if u != "" {
				ytDlpPath := getYTlpPath()
				outputPattern := filepath.Join("uploads", "%(title)s.%(ext)s")
				argsName := []string{"--get-filename", "-o", outputPattern, "--restrict-filenames", u}
				if config.Proxy != "" {
					argsName = append([]string{"--proxy", config.Proxy}, argsName...)
				}
				cmdName := exec.Command(ytDlpPath, argsName...)
				outName, err := cmdName.Output()
				if err != nil {
					updateTaskError(tID, "Failed to get filename: "+err.Error())
					return
				}
				currentDst = strings.TrimSpace(string(outName))
				currentFilename = filepath.Base(currentDst)

				argsDl := []string{"-o", outputPattern, "--restrict-filenames", u}
				if config.Proxy != "" {
					argsDl = append([]string{"--proxy", config.Proxy}, argsDl...)
				}
				cmd := exec.Command(ytDlpPath, argsDl...)
				if err := cmd.Run(); err != nil {
					updateTaskError(tID, "Download failed: "+err.Error())
					return
				}
			}

			ext := filepath.Ext(currentFilename)
			outputName := "out_" + currentFilename
			outputPath := filepath.Join("uploads", outputName)

			switch pType {
			case "video_compress":
				outputName = "compressed_" + currentFilename
				outputPath = filepath.Join("uploads", outputName)
				errProcessing = transcoding.SuperCompressVideo(currentDst, outputPath, cVal, wVal, fVal)
			case "video_to_gif":
				outputName = filepath.Base(currentFilename[:len(currentFilename)-len(ext)]) + ".gif"
				outputPath = filepath.Join("uploads", outputName)
				errProcessing = transcoding.VideoToGif(currentDst, outputPath, wVal, fVal)
			case "video_to_audio":
				outputName = filepath.Base(currentFilename[:len(currentFilename)-len(ext)]) + ".mp3"
				outputPath = filepath.Join("uploads", outputName)
				errProcessing = transcoding.VideoToAudio(currentDst, outputPath, "128k")
			case "image_compress":
				outputName = "compressed_" + currentFilename
				outputPath = filepath.Join("uploads", outputName)
				errProcessing = transcoding.CompressImageResize(currentDst, outputPath, qVal, wVal, 0)
			}

			if errProcessing != nil {
				updateTaskError(tID, "Processing failed: "+errProcessing.Error())
				return
			}

			tasksMu.Lock()
			tasks[tID].Status = "completed"
			tasks[tID].OutputFile = outputName
			tasksMu.Unlock()
		}(taskID, url, processType, dst, filename, crf, width, fps, quality)

		c.JSON(http.StatusOK, gin.H{"task_id": taskID})
	})

	// Загружаем HTML шаблоны
	r.LoadHTMLGlob("templates/*")

	// Создаём папку uploads
	os.MkdirAll("uploads", os.ModePerm)

	fmt.Printf("Сервер запущен на http://0.0.0.0:%d\n", config.Port)
	r.Run(fmt.Sprintf("0.0.0.0:%d", config.Port))
}

func cleanupRoutine() {
	if config.CleanupMinutes <= 0 {
		return
	}
	ticker := time.NewTicker(1 * time.Minute)
	go func() {
		for range ticker.C {
			files, err := os.ReadDir("uploads")
			if err != nil {
				continue
			}
			now := time.Now()
			for _, f := range files {
				info, err := f.Info()
				if err != nil {
					continue
				}
				if now.Sub(info.ModTime()) > time.Duration(config.CleanupMinutes)*time.Minute {
					path := filepath.Join("uploads", f.Name())
					fmt.Printf(">>> Удаление старого файла: %s\n", path)
					os.Remove(path)
				}
			}
		}
	}()
}
