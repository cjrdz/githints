package cmd

import (
	"github.com/spf13/cobra"

	"example.com/svc/internal/api"
)

var rootCmd = &cobra.Command{
	Use:   "svc",
	Short: "The service",
}

var serveCmd = &cobra.Command{
	Use:   "serve [flags]",
	Short: "Run the HTTP server",
	RunE: func(cmd *cobra.Command, args []string) error {
		_ = api.Routes
		return nil
	},
}
