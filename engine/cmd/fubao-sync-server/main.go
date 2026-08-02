package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"fubao.ccvar.com/engine/internal/syncserver"
)

var version = "dev"

func main() {
	listen := flag.String("listen", "127.0.0.1:8788", "HTTP listen address")
	dataPath := flag.String("data", "/var/lib/fubao-sync/fubao-sync.db", "SQLite database path")
	tokenFile := flag.String("enrollment-token-file", "/etc/fubao-sync/enrollment.token", "device enrollment token file")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}

	enrollmentToken := strings.TrimSpace(os.Getenv("FUBAO_SYNC_ENROLLMENT_TOKEN"))
	if enrollmentToken == "" {
		content, err := os.ReadFile(filepath.Clean(*tokenFile))
		if err != nil {
			log.Fatalf("读取设备注册令牌失败: %v", err)
		}
		enrollmentToken = strings.TrimSpace(string(content))
	}
	store, err := syncserver.OpenStore(*dataPath)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	service, err := syncserver.New(store, enrollmentToken, version)
	if err != nil {
		log.Fatal(err)
	}

	httpServer := &http.Server{
		Addr:              *listen,
		Handler:           service.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(ctx)
	}()
	log.Printf("fubao-sync-server %s listening on %s", version, *listen)
	if err := httpServer.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
