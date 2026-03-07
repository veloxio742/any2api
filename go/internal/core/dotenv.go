package core

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func LoadDotEnv(paths ...string) error {
	for _, path := range paths {
		trimmedPath := strings.TrimSpace(path)
		if trimmedPath == "" {
			continue
		}
		if err := loadDotEnvFile(trimmedPath); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
	}
	return nil
}

func loadDotEnvFile(path string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := parseDotEnv(content); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

func parseDotEnv(content []byte) error {
	scanner := bufio.NewScanner(bytes.NewReader(content))
	for lineNo := 1; scanner.Scan(); lineNo++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		separator := strings.IndexRune(line, '=')
		if separator <= 0 {
			return fmt.Errorf("invalid line %d", lineNo)
		}
		key := strings.TrimSpace(line[:separator])
		if key == "" {
			return fmt.Errorf("invalid line %d", lineNo)
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		value := parseDotEnvValue(strings.TrimSpace(line[separator+1:]))
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("set %s: %w", key, err)
		}
	}
	return scanner.Err()
}

func parseDotEnvValue(raw string) string {
	if len(raw) >= 2 {
		if raw[0] == '"' && raw[len(raw)-1] == '"' {
			if unquoted, err := strconv.Unquote(raw); err == nil {
				return unquoted
			}
		}
		if raw[0] == '\'' && raw[len(raw)-1] == '\'' {
			return raw[1 : len(raw)-1]
		}
	}
	return raw
}
