package main

import (
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"runtime"

	"any2api-go/internal/core"
	apphttp "any2api-go/internal/http"
)

func main() {
	if err := loadDotEnv(); err != nil {
		log.Fatalf("load .env: %v", err)
	}
	cfg := core.LoadAppConfigFromEnv()
	runtimeManager, err := core.NewRuntimeManager(filepath.Join(cfg.DataDir, "admin.json"), cfg)
	if err != nil {
		log.Fatalf("initialize runtime manager: %v", err)
	}
	handler := apphttp.NewHandlerWithRuntime(runtimeManager)
	addr := fmt.Sprintf(":%d", cfg.Port)
	log.Printf("any2api-go listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, handler.Routes()))
}

func loadDotEnv() error {
	paths := []string{".env"}
	if _, file, _, ok := runtime.Caller(0); ok {
		paths = append(paths, filepath.Join(filepath.Dir(file), "..", "..", ".env"))
	}
	return core.LoadDotEnv(paths...)
}
