package main

import (
	"log"

	"github.com/spf13/cobra"
)

// @title           Armi File Manager API
// @version         1.0
// @description     Armi PDF/Word/Excel/PPT/TXT/RTF 檔案管理器 RESTful API。
// @BasePath        /api/v1
// @securityDefinitions.basic  BasicAuth
// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
func main() {
	if err := newRootCommand().Execute(); err != nil {
		log.Fatalf("command failed: %v", err)
	}
}

func newRootCommand() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "armi",
		Short: "Armi service",
	}

	rootCmd.AddCommand(
		newServeCommand(),
		newConsumerCommand(),
		newClientCommand(),
	)

	return rootCmd
}
