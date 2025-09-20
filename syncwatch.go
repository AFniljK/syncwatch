package main

import (
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/google/uuid"
)

type Handler struct {
	channels map[string]chan string
	mx       sync.Mutex
}

func init_logging() {
	log_path, exists := os.LookupEnv("LOG_DIR")
	if !exists {
		log_path = "./logs"
	}
	log_file, err := os.OpenFile(filepath.Join(log_path, "/syncwatch.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		panic(err)
	}
	writers := io.MultiWriter(os.Stdout, log_file)
	logger := slog.New(slog.NewTextHandler(writers, nil))
	slog.SetDefault(logger)
}

func main() {
	init_logging()
	port, exists := os.LookupEnv("PORT")
	if !exists {
		port = "3000"
	}
	port = ":" + port
	content_dir, exists := os.LookupEnv("CONTENT_DIR")
	if !exists {
		content_dir = "./content"
	}

	handler := Handler{channels: make(map[string]chan string), mx: sync.Mutex{}}
	slog.Info("serving content from", slog.String("path", content_dir))
	file_server := http.FileServer(http.Dir(content_dir))
	file_handler := http.StripPrefix("/content/", file_server)
	http.HandleFunc("/content/", func(w http.ResponseWriter, r *http.Request) {
		file_handler.ServeHTTP(w, r)
	})

	http.HandleFunc("/", handler.nigma_handler)
	http.HandleFunc("GET /resume", handler.playback_handler("resume"))
	http.HandleFunc("GET /hold", handler.playback_handler("hold"))
	http.HandleFunc("GET /sync/{time}", func(w http.ResponseWriter, r *http.Request) {
		sync := r.PathValue("time")
		handler.playback_handler(fmt.Sprintf("seek:%s", sync))(w, r)
	})
	http.HandleFunc("/event", handler.event_handler)

	slog.Info("listening and serving", slog.String("PORT", port))
	panic(http.ListenAndServe(port, nil))
}

func (handler *Handler) playback_handler(message string) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("ID")
		if err != nil {
			return
		}
		client_addr := cookie.Value
		slog.Info(message, slog.String("client", client_addr))
		handler.mx.Lock()
		for recv_addr, ch := range handler.channels {
			if recv_addr == client_addr {
				continue
			}
			ch <- fmt.Sprintf("data: %s\n\n", message)
		}
		handler.mx.Unlock()
	}
}

func (handler *Handler) nigma_handler(w http.ResponseWriter, r *http.Request) {
	_, err := r.Cookie("ID")
	if err != nil {
		client_addr := uuid.NewString()
		http.SetCookie(w, &http.Cookie{Name: "ID", Value: client_addr})
	}
	path, exists := os.LookupEnv("TEMPLATE_DIR")
	if !exists {
		path = "./templates"
	}
	path = filepath.Join(path, "index.html")
	nigma_template := template.Must(template.ParseFiles(path))
	nigma_template.Execute(w, nil)
}

func (handler *Handler) event_handler(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("ID")
	if err != nil {
		return
	}
	client_addr := cookie.Value

	var channel chan string
	var exists bool

	handler.mx.Lock()
	channel, exists = handler.channels[client_addr]
	handler.mx.Unlock()

	if !exists {
		slog.Info("creating connection", slog.String("client", client_addr))
		channel = make(chan string)
		handler.mx.Lock()
		handler.channels[client_addr] = channel
		handler.mx.Unlock()
	}

	w.Header().Set("Content-Type", "text/event-stream")
	rc := http.NewResponseController(w)

channeling:
	for message := range channel {
		if _, err := w.Write([]byte(message)); err != nil {
			slog.Error("writing message", "error", err.Error())
		}
		err := rc.Flush()
		if err != nil {
			slog.Info("deleting connection", slog.String("client", client_addr))
			handler.mx.Lock()
			close(channel)
			delete(handler.channels, client_addr)
			handler.mx.Unlock()
			break channeling
		}
	}
}
