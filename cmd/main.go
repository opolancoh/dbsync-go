package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "dbsync",
	Short: "Database migration tool",
}

func main() {
	rootCmd.AddCommand(tuiCmd)
	rootCmd.AddCommand(backupCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
