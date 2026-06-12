package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	armiClient "github.com/star-inc/armi/internal/client"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

type clientConfigContextKey struct{}

var supportedUploadExts = map[string]struct{}{
	".pdf":  {},
	".doc":  {},
	".docx": {},
	".xls":  {},
	".xlsx": {},
	".ppt":  {},
	".pptx": {},
	".txt":  {},
	".rtf":  {},
	".md":   {},
}

func newClientCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "client",
		Short: "REST client for Armi",
	}

	cmd.PersistentFlags().String("base-url", "http://127.0.0.1:8080", "Armi server base URL")
	cmd.PersistentFlags().String("username", "", "Basic auth username")
	cmd.PersistentFlags().String("password", "", "Basic auth password")
	cmd.PersistentFlags().String("token", "", "Bearer token")
	cmd.PersistentFlags().Duration("timeout", 30*time.Second, "HTTP request timeout")

	clientConfig := viper.New()
	clientConfig.SetEnvPrefix("ARMI")
	clientConfig.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	clientConfig.AutomaticEnv()

	for _, key := range []string{"base-url", "username", "password", "token", "timeout"} {
		if err := clientConfig.BindPFlag(key, cmd.PersistentFlags().Lookup(key)); err != nil {
			panic(err)
		}
	}

	cmd.PersistentPreRun = func(cmd *cobra.Command, _ []string) {
		ctx := context.WithValue(cmd.Context(), clientConfigContextKey{}, clientConfig)
		cmd.SetContext(ctx)
	}

	cmd.AddCommand(
		newClientHealthCommand(),
		newClientUserCommand(),
		newClientFileCommand(),
		newClientUploadCommand(),
	)
	return cmd
}

func newClientUploadCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "upload",
		Short: "Upload files or folders to Armi",
	}

	cmd.PersistentFlags().String("description", "", "Upload description")
	cmd.PersistentFlags().StringSlice("tags", nil, "Upload tags, separated by commas or repeated flags")
	cmd.PersistentFlags().StringSlice("group-ids", nil, "Group IDs, separated by commas or repeated flags")

	fileCmd := &cobra.Command{
		Use:   "file",
		Short: "Upload a single file",
		RunE:  runClientUploadFile,
	}
	fileCmd.Flags().String("path", "", "Path to the file to upload")
	if err := fileCmd.MarkFlagRequired("path"); err != nil {
		panic(err)
	}

	folderCmd := &cobra.Command{
		Use:   "folder",
		Short: "Upload a folder recursively",
		RunE:  runClientUploadFolder,
	}
	folderCmd.Flags().String("path", "", "Path to the folder to upload")
	if err := folderCmd.MarkFlagRequired("path"); err != nil {
		panic(err)
	}

	cmd.AddCommand(fileCmd, folderCmd)
	return cmd
}

func runClientUploadFile(cmd *cobra.Command, _ []string) error {
	path, err := cmd.Flags().GetString("path")
	if err != nil {
		return err
	}

	client, err := buildArmiClient(cmd)
	if err != nil {
		return err
	}

	opts := uploadOptionsFromCommand(cmd)
	resp, err := client.UploadFile(cmd.Context(), path, opts)
	if err != nil {
		return err
	}

	fmt.Fprintf(cmd.OutOrStdout(), "uploaded %s -> %s (%s)\n", path, resp.ID, resp.Filename)
	return nil
}

func runClientUploadFolder(cmd *cobra.Command, _ []string) error {
	rootPath, err := cmd.Flags().GetString("path")
	if err != nil {
		return err
	}

	client, err := buildArmiClient(cmd)
	if err != nil {
		return err
	}

	files, skipped, err := collectUploadTargets(rootPath)
	if err != nil {
		return err
	}

	if len(files) == 0 {
		return fmt.Errorf("no supported files found under %s", rootPath)
	}

	baseOpts := uploadOptionsFromCommand(cmd)
	ctx := cmd.Context()
	var failed int
	for _, path := range files {
		opts := baseOpts
		if strings.TrimSpace(opts.Description) == "" {
			opts.Description = armiClient.NormalizePath(rootPath, path)
		}

		resp, uploadErr := client.UploadFile(ctx, path, opts)
		if uploadErr != nil {
			failed++
			fmt.Fprintf(cmd.ErrOrStderr(), "failed %s: %v\n", path, uploadErr)
			continue
		}

		fmt.Fprintf(cmd.OutOrStdout(), "uploaded %s -> %s (%s)\n", path, resp.ID, resp.Filename)
	}

	if skipped > 0 {
		fmt.Fprintf(cmd.ErrOrStderr(), "skipped %d unsupported file(s)\n", skipped)
	}

	if failed > 0 {
		return fmt.Errorf("%d file upload(s) failed", failed)
	}

	return nil
}

func buildArmiClient(cmd *cobra.Command) (*armiClient.Client, error) {
	clientConfig, err := clientConfiguration(cmd)
	if err != nil {
		return nil, err
	}

	return armiClient.New(armiClient.Config{
		BaseURL:     clientConfig.GetString("base-url"),
		Username:    clientConfig.GetString("username"),
		Password:    clientConfig.GetString("password"),
		BearerToken: clientConfig.GetString("token"),
		Timeout:     clientConfig.GetDuration("timeout"),
	})
}

func clientConfiguration(cmd *cobra.Command) (*viper.Viper, error) {
	clientConfig, ok := cmd.Context().Value(clientConfigContextKey{}).(*viper.Viper)
	if !ok {
		return nil, fmt.Errorf("client configuration is not initialized")
	}
	return clientConfig, nil
}

func uploadOptionsFromCommand(cmd *cobra.Command) armiClient.UploadOptions {
	description, _ := cmd.Flags().GetString("description")
	tags, _ := cmd.Flags().GetStringSlice("tags")
	groupIDs, _ := cmd.Flags().GetStringSlice("group-ids")

	return armiClient.UploadOptions{
		Description: description,
		Tags:        tags,
		GroupIDs:    groupIDs,
	}
}

func collectUploadTargets(rootPath string) ([]string, int, error) {
	info, err := os.Stat(rootPath)
	if err != nil {
		return nil, 0, err
	}

	if !info.IsDir() {
		if isSupportedUploadPath(rootPath) {
			return []string{rootPath}, 0, nil
		}
		return nil, 0, fmt.Errorf("unsupported file extension: %s", filepath.Ext(rootPath))
	}

	var files []string
	skipped := 0
	err = filepath.WalkDir(rootPath, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if !isSupportedUploadPath(path) {
			skipped++
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return nil, 0, err
	}

	sort.Strings(files)
	return files, skipped, nil
}

func isSupportedUploadPath(path string) bool {
	_, ok := supportedUploadExts[strings.ToLower(filepath.Ext(path))]
	return ok
}
