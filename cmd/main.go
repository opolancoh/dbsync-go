package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "dbsync",
	Short: "Database migration tool",
	// A failure inside a command is a runtime error, not a usage mistake;
	// printing the flag usage above it only buries the message. Errors are
	// silenced here too because main() already prints them to stderr.
	SilenceUsage:  true,
	SilenceErrors: true,
}

func main() {
	rootCmd.AddCommand(tuiCmd)
	rootCmd.AddCommand(backupCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
