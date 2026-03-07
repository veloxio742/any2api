package cursor

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

func embeddedCursorJSAssets() (string, string, error) {
	candidates := []string{
		filepath.Join("..", "..", "cursor2api-go", "jscode"),
		filepath.Join("..", "..", "..", "cursor2api-go", "jscode"),
	}
	if _, file, _, ok := runtime.Caller(0); ok {
		baseDir := filepath.Dir(file)
		candidates = append([]string{filepath.Join(baseDir, "..", "..", "..", "..", "..", "cursor2api-go", "jscode")}, candidates...)
	}
	for _, dir := range candidates {
		if mainJS, envJS, err := loadCursorJSAssetsFromDir(dir); err == nil {
			return mainJS, envJS, nil
		}
	}
	return "", "", fmt.Errorf("cursor jscode assets not found")
}

func loadCursorJSAssetsFromDir(dir string) (string, string, error) {
	mainJS, err := os.ReadFile(filepath.Join(dir, "main.js"))
	if err != nil {
		return "", "", err
	}
	envJS, err := os.ReadFile(filepath.Join(dir, "env.js"))
	if err != nil {
		return "", "", err
	}
	return string(mainJS), string(envJS), nil
}
