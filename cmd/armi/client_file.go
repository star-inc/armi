package main

import (
	"fmt"
	"os"
	"path/filepath"

	armiClient "github.com/star-inc/armi/internal/client"
	"github.com/star-inc/armi/pkgs/contract"
	"github.com/spf13/cobra"
)

func newClientFileCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "file",
		Short: "Manage and search Armi files",
	}
	cmd.AddCommand(
		newClientFileListCommand(),
		newClientFileDownloadCommand(),
		newClientFileMetadataCommand(),
		newClientFileUpdateCommand(),
		newClientFileDeleteCommand(),
		newClientFileSearchCommand(),
	)
	return cmd
}

func newClientFileListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List accessible files",
		RunE: func(cmd *cobra.Command, _ []string) error {
			tag, _ := cmd.Flags().GetString("tag")
			page, _ := cmd.Flags().GetInt("page")
			pageSize, _ := cmd.Flags().GetInt("page-size")

			client, err := buildArmiClient(cmd)
			if err != nil {
				return err
			}
			result, err := client.ListFiles(cmd.Context(), armiClient.ListFilesOptions{
				Tag:      tag,
				Page:     page,
				PageSize: pageSize,
			})
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), result)
		},
	}
	cmd.Flags().String("tag", "", "Filter by tag")
	cmd.Flags().Int("page", 1, "Page number")
	cmd.Flags().Int("page-size", 20, "Items per page, maximum 100")
	return cmd
}

func newClientFileDownloadCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "download",
		Short: "Download a file",
		RunE: func(cmd *cobra.Command, _ []string) error {
			fileID, _ := cmd.Flags().GetString("id")
			output, _ := cmd.Flags().GetString("output")

			client, err := buildArmiClient(cmd)
			if err != nil {
				return err
			}
			result, err := client.DownloadFile(cmd.Context(), fileID)
			if err != nil {
				return err
			}

			target := output
			if target == "" {
				target = result.Filename
			} else if info, statErr := os.Stat(target); statErr == nil && info.IsDir() {
				target = filepath.Join(target, result.Filename)
			}
			if err := os.WriteFile(target, result.Data, 0o644); err != nil {
				return fmt.Errorf("write download %q: %w", target, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "downloaded %s -> %s\n", fileID, target)
			return nil
		},
	}
	cmd.Flags().String("id", "", "File ID")
	cmd.Flags().StringP("output", "o", "", "Output file or existing directory")
	_ = cmd.MarkFlagRequired("id")
	return cmd
}

func newClientFileMetadataCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "metadata",
		Short: "Get file metadata",
		RunE: func(cmd *cobra.Command, _ []string) error {
			fileID, _ := cmd.Flags().GetString("id")
			client, err := buildArmiClient(cmd)
			if err != nil {
				return err
			}
			result, err := client.GetFileMetadata(cmd.Context(), fileID)
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), result)
		},
	}
	cmd.Flags().String("id", "", "File ID")
	_ = cmd.MarkFlagRequired("id")
	return cmd
}

func newClientFileUpdateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update file metadata",
		RunE: func(cmd *cobra.Command, _ []string) error {
			fileID, _ := cmd.Flags().GetString("id")
			var req contract.UpdateFileMetadataRequest

			if cmd.Flags().Changed("filename") {
				value, _ := cmd.Flags().GetString("filename")
				req.Filename = &value
			}
			if cmd.Flags().Changed("description") {
				value, _ := cmd.Flags().GetString("description")
				req.Description = &value
			}
			if cmd.Flags().Changed("tags") {
				values, _ := cmd.Flags().GetStringSlice("tags")
				req.Tags = cleanStringSlice(values)
			}
			if cmd.Flags().Changed("group-ids") {
				values, _ := cmd.Flags().GetStringSlice("group-ids")
				req.GroupIDs = cleanStringSlice(values)
			}
			if req.Filename == nil && req.Description == nil && req.Tags == nil && req.GroupIDs == nil {
				return fmt.Errorf("at least one metadata field is required")
			}

			client, err := buildArmiClient(cmd)
			if err != nil {
				return err
			}
			result, err := client.UpdateFileMetadata(cmd.Context(), fileID, req)
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), result)
		},
	}
	cmd.Flags().String("id", "", "File ID")
	cmd.Flags().String("filename", "", "New filename")
	cmd.Flags().String("description", "", "New description")
	cmd.Flags().StringSlice("tags", nil, "Replacement tags; pass an empty value to clear")
	cmd.Flags().StringSlice("group-ids", nil, "Replacement group IDs; pass an empty value to clear")
	_ = cmd.MarkFlagRequired("id")
	return cmd
}

func newClientFileDeleteCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete a file",
		RunE: func(cmd *cobra.Command, _ []string) error {
			fileID, _ := cmd.Flags().GetString("id")
			client, err := buildArmiClient(cmd)
			if err != nil {
				return err
			}
			result, err := client.DeleteFile(cmd.Context(), fileID)
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), result)
		},
	}
	cmd.Flags().String("id", "", "File ID")
	_ = cmd.MarkFlagRequired("id")
	return cmd
}

func newClientFileSearchCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "search",
		Short: "Search files semantically",
		RunE: func(cmd *cobra.Command, _ []string) error {
			query, _ := cmd.Flags().GetString("query")
			limit, _ := cmd.Flags().GetInt("limit")
			nlpExpansion, _ := cmd.Flags().GetBool("nlp-expansion")
			expansionNum, _ := cmd.Flags().GetInt("expansion-num")

			client, err := buildArmiClient(cmd)
			if err != nil {
				return err
			}
			result, err := client.SearchFiles(cmd.Context(), armiClient.SearchFilesOptions{
				Query:        query,
				Limit:        limit,
				NLPExpansion: nlpExpansion,
				ExpansionNum: expansionNum,
			})
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), result)
		},
	}
	cmd.Flags().StringP("query", "q", "", "Search query")
	cmd.Flags().Int("limit", 5, "Maximum result count")
	cmd.Flags().Bool("nlp-expansion", false, "Enable NLP query expansion")
	cmd.Flags().Int("expansion-num", 3, "Number of expanded queries")
	_ = cmd.MarkFlagRequired("query")
	return cmd
}
