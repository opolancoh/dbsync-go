package main

import (
	"fmt"
	"os"
)

var stepNames = []string{"Inspect", "Backup", "Analyze", "Transfer"}

func printStep(step int) {
	fmt.Printf("Step %d of %d: %s\n", step, len(stepNames), stepNames[step-1])
	fmt.Println("─────────────────────────────────────────────────────")
	fmt.Println()
}

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
