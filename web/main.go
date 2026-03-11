package main

import (
	"bufio"
	"comp/transcoding"
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
	"github.com/redis/go-redis/v9"
)

var (
	ctx         = context.Background()
	rdb         *redis.Client
	storagePath = "/tmp/app"
)

type Config struct {
	CleanupMinutes int `json:"cleanup_minutes"`
	Port           int `json:"port"`
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
	Progress   int    `json:"progress"`
	OutputFile string `json:"output_file"`
	Error      string `json:"error"`
}

func initRedis() {
	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}
	rdb = redis.NewClient(&redis.Options{
		Addr: redisAddr,
	})
}

func setTask(task *TaskStatus) {
	data, _ := json.Marshal(task)
	rdb.Set(ctx, "task:"+task.ID, data, time.Hour*24)
}

func getTask(id string) (*TaskStatus, bool) {
	data, err := rdb.Get(ctx, "task:"+id).Bytes()
	if err != nil {
		return nil, false
	}
	var task TaskStatus
	json.Unmarshal(data, &task)
	return &task, true
}

func updateTaskProgress(id string, progress int) {
	if task, ok := getTask(id); ok {
		task.Progress = progress
		setTask(task)
	}
}

func updateTaskError(id, errMsg string) {
	if task, ok := getTask(id); ok {
		task.Status = "failed"
		task.Error = errMsg
		setTask(task)
	}
}

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

func main() {
	loadConfig()
	initRedis()
	cleanupRoutine()

	// Обновление yt-dlp при старте
	fmt.Println("Updating yt-dlp...")
	updateCmd := exec.Command(getYTlpPath(), "-U")
	if out, err := updateCmd.CombinedOutput(); err != nil {
		fmt.Printf("Failed to update yt-dlp: %v, output: %s\n", err, string(out))
	} else {
		fmt.Printf("yt-dlp update output: %s\n", string(out))
	}

	gin.SetMode(gin.ReleaseMode)
	r := gin.Default()

	// Статика
	r.Static("/static", "./static")
	r.Static("/uploads", storagePath) // Отдаем файлы из tmpfs

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
		args := []string{"--user-agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36", "--dump-json", "--restrict-filenames", url}

		// Add cookies if available
		if _, err := os.Stat("cookies.txt"); err == nil {
			args = append([]string{"--cookies", "cookies.txt"}, args...)
		}

		cmd := exec.Command(ytDlpPath, args...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to get video info: %v. Output: %s", err, string(out))})
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
		task, ok := getTask(id)

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

		taskID := uuid.New().String()
		jobDir := filepath.Join(storagePath, taskID)
		os.MkdirAll(jobDir, os.ModePerm)

		var dst string
		var filename string

		if url == "" {
			file, err := c.FormFile("file")
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "File or URL is required"})
				return
			}
			filename = file.Filename
			dst = filepath.Join(jobDir, filename)
			if err := c.SaveUploadedFile(file, dst); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save file"})
				return
			}
		}

		setTask(&TaskStatus{ID: taskID, Status: "processing"})

		go func(tID, u, pType, d, fName, jDir string, cVal, wVal, fVal, qVal int) {
			var errProcessing error
			currentDst := d
			currentFilename := fName

			if u != "" {
				fmt.Printf("[%s] Начинаем загрузку по URL: %s\n", tID, u)
				ytDlpPath := getYTlpPath()
				outputPattern := filepath.Join(jDir, "%(title)s.%(ext)s")
				argsName := []string{"--user-agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36", "--get-filename", "-o", outputPattern, "--restrict-filenames", u}

				// Add cookies if available
				if _, err := os.Stat("cookies.txt"); err == nil {
					argsName = append([]string{"--cookies", "cookies.txt"}, argsName...)
				}

				cmdName := exec.Command(ytDlpPath, argsName...)
				outName, err := cmdName.CombinedOutput()
				if err != nil {
					updateTaskError(tID, fmt.Sprintf("Failed to get filename: %v. Output: %s", err, string(outName)))
					return
				}
				currentDst = strings.TrimSpace(string(outName))
				// If there are multiple lines (yt-dlp warnings), take the last one which should be the filename
				lines := strings.Split(currentDst, "\n")
				if len(lines) > 1 {
					currentDst = strings.TrimSpace(lines[len(lines)-1])
				}
				currentFilename = filepath.Base(currentDst)
				argsDl := []string{"--user-agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36", "-o", outputPattern, "--restrict-filenames", u}

				// Add cookies if available
				if _, err := os.Stat("cookies.txt"); err == nil {
					argsDl = append([]string{"--cookies", "cookies.txt"}, argsDl...)
				}

				cmd := exec.Command(ytDlpPath, argsDl...)
				stdout, _ := cmd.StdoutPipe()
				cmd.Stderr = cmd.Stdout
				if err := cmd.Start(); err != nil {
					updateTaskError(tID, fmt.Sprintf("Download failed: %v", err))
					return
				}

				// Парсим прогресс yt-dlp
				scanner := bufio.NewScanner(stdout)
				for scanner.Scan() {
					line := scanner.Text()
					if strings.Contains(line, "%") {
						parts := strings.Fields(line)
						for _, p := range parts {
							if strings.HasSuffix(p, "%") {
								percStr := strings.TrimSuffix(p, "%")
								if perc, err := strconv.ParseFloat(percStr, 64); err == nil {
									updateTaskProgress(tID, int(perc*0.5))
								}
								break
							}
						}
					}
				}
				cmd.Wait()

				// После скачивания проверяем, какой файл реально появился в директории,
				// так как yt-dlp может изменить расширение при склейке потоков.
				files, err := os.ReadDir(jDir)
				if err == nil && len(files) > 0 {
					for _, f := range files {
						if !f.IsDir() && !strings.HasPrefix(f.Name(), "out_") &&
							!strings.HasPrefix(f.Name(), "compressed_") &&
							filepath.Ext(f.Name()) != ".gif" &&
							filepath.Ext(f.Name()) != ".mp3" &&
							f.Name() != "palette.png" {
							currentDst = filepath.Join(jDir, f.Name())
							currentFilename = f.Name()
							fmt.Printf("[%s] Скачан файл: %s\n", tID, currentFilename)
							break
						}
					}
				}
			}

			fmt.Printf("[%s] Начало обработки: %s, тип: %s\n", tID, currentFilename, pType)
			ext := filepath.Ext(currentFilename)
			outputName := "out_" + currentFilename
			outputPath := filepath.Join(jDir, outputName)

			switch pType {
			case "video_compress":
				outputName = "compressed_" + currentFilename
				outputPath = filepath.Join(jDir, outputName)
				progressChan := make(chan float64)
				go func() {
					for p := range progressChan {
						// Если был URL, то скачивание это 50%, обработка это еще 50%
						if u != "" {
							updateTaskProgress(tID, 50+int(p*0.5))
						} else {
							updateTaskProgress(tID, int(p))
						}
					}
				}()
				errProcessing = transcoding.SuperCompressVideo(currentDst, outputPath, cVal, wVal, fVal, progressChan)
			case "video_to_gif":
				outputName = filepath.Base(currentFilename[:len(currentFilename)-len(ext)]) + ".gif"
				outputPath = filepath.Join(jDir, outputName)
				progressChan := make(chan float64)
				go func() {
					for p := range progressChan {
						if u != "" {
							updateTaskProgress(tID, 50+int(p*0.5))
						} else {
							updateTaskProgress(tID, int(p))
						}
					}
				}()
				errProcessing = transcoding.VideoToGifWithProgress(currentDst, outputPath, wVal, fVal, progressChan)
			case "video_to_audio":
				outputName = filepath.Base(currentFilename[:len(currentFilename)-len(ext)]) + ".mp3"
				outputPath = filepath.Join(jDir, outputName)
				progressChan := make(chan float64)
				go func() {
					for p := range progressChan {
						if u != "" {
							updateTaskProgress(tID, 50+int(p*0.5))
						} else {
							updateTaskProgress(tID, int(p))
						}
					}
				}()
				errProcessing = transcoding.VideoToAudioWithProgress(currentDst, outputPath, "128k", progressChan)
			case "image_compress":
				outputName = "compressed_" + currentFilename
				outputPath = filepath.Join(jDir, outputName)
				updateTaskProgress(tID, 50)
				errProcessing = transcoding.CompressImageResize(currentDst, outputPath, qVal, wVal, 0)
				if errProcessing == nil {
					updateTaskProgress(tID, 100)
				}
			}

			if errProcessing != nil {
				fmt.Printf("[%s] Ошибка обработки: %v\n", tID, errProcessing)
				updateTaskError(tID, "Processing failed: "+errProcessing.Error())
				return
			}

			fmt.Printf("[%s] Обработка успешно завершена: %s\n", tID, outputName)
			if task, ok := getTask(tID); ok {
				task.Status = "completed"
				task.OutputFile = taskID + "/" + outputName // Путь относительно /uploads/
				setTask(task)
			}
		}(taskID, url, processType, dst, filename, jobDir, crf, width, fps, quality)

		c.JSON(http.StatusOK, gin.H{"task_id": taskID})
	})

	// Загружаем HTML шаблоны
	r.LoadHTMLGlob("templates/*")

	// Создаём папку uploads
	os.MkdirAll(storagePath, os.ModePerm)

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
			dirs, err := os.ReadDir(storagePath)
			if err != nil {
				continue
			}
			now := time.Now()
			for _, d := range dirs {
				if !d.IsDir() {
					continue
				}
				info, err := d.Info()
				if err != nil {
					continue
				}
				if now.Sub(info.ModTime()) > time.Duration(config.CleanupMinutes)*time.Minute {
					path := filepath.Join(storagePath, d.Name())
					fmt.Printf(">>> Удаление старой директории задания: %s\n", path)
					os.RemoveAll(path)
				}
			}
		}
	}()
}
