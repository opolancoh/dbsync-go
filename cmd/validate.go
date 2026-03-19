package main

import (
	"fmt"
	"os"
)

func validateDir(path, label string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%s not found: %s", label, path)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory: %s", label, path)
	}
	return nil
}

func validateFile(path, label string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%s not found: %s", label, path)
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory, expected a file: %s", label, path)
	}
	return nil
}
