package http

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/gin-gonic/gin"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/star-inc/armi/internal/usecase"
	"github.com/star-inc/armi/pkgs/user"
)

// MCPHandler handles Model Context Protocol requests over Streamable HTTP.
type MCPHandler struct {
	fileUsecase          *usecase.FileUsecase
	StreamableHTTPServer *server.StreamableHTTPServer
}

// NewMCPHandler initializes the MCP server and registers tools.
func NewMCPHandler(fileUsecase *usecase.FileUsecase) *MCPHandler {
	mcpServer := server.NewMCPServer("armi-mcp-server", "1.0.0")

	h := &MCPHandler{
		fileUsecase: fileUsecase,
	}

	// 1. Register list_files tool
	listFilesTool := mcp.NewTool("list_files",
		mcp.WithDescription("列出當前使用者可存取的檔案清單"),
		mcp.WithString("tag", mcp.Description("過濾特定的檔案標籤（選填）")),
		mcp.WithNumber("page", mcp.Description("頁碼，從 1 開始"), mcp.DefaultNumber(1)),
		mcp.WithNumber("page_size", mcp.Description("每頁筆數，最多 100 筆"), mcp.DefaultNumber(20)),
	)
	mcpServer.AddTool(listFilesTool, h.handleListFiles)

	// 2. Register search_files tool
	searchFilesTool := mcp.NewTool("search_files",
		mcp.WithDescription("對使用者的檔案進行向量語義搜尋"),
		mcp.WithString("query", mcp.Required(), mcp.Description("搜尋關鍵字或查詢語句")),
		mcp.WithNumber("limit", mcp.Description("返回的最多結果數量"), mcp.DefaultNumber(5)),
		mcp.WithBoolean("nlp_expansion", mcp.Description("是否啟用自然語言加速搜尋"), mcp.DefaultBool(false)),
		mcp.WithNumber("expansion_num", mcp.Description("生成的搜尋問法數量（預設 3）"), mcp.DefaultNumber(3)),
	)
	mcpServer.AddTool(searchFilesTool, h.handleSearchFiles)

	// 3. Register read_file tool
	readFileTool := mcp.NewTool("read_file",
		mcp.WithDescription("讀取指定檔案 ID 的內容（嘗試提取純文字並返回）"),
		mcp.WithString("file_id", mcp.Required(), mcp.Description("檔案的唯一識別 ID")),
	)
	mcpServer.AddTool(readFileTool, h.handleReadFile)

	h.StreamableHTTPServer = server.NewStreamableHTTPServer(mcpServer)

	return h
}

type mcpContextKey string

const userCtxKey mcpContextKey = "user"

// MCPContextMiddleware extracts the user from Gin's Context and injects it into http.Request Context
// so that it can be retrieved within mcp-go ToolHandlers.
func MCPContextMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		val, ok := c.Get("user")
		if ok {
			dbUser, ok := val.(*user.User)
			if ok {
				ctx := context.WithValue(c.Request.Context(), userCtxKey, dbUser)
				c.Request = c.Request.WithContext(ctx)
			}
		}
		c.Next()
	}
}

func (h *MCPHandler) handleListFiles(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	dbUser, ok := ctx.Value(userCtxKey).(*user.User)
	if !ok {
		slog.Warn("MCP tool list_files invoked without authenticated user context")
		return nil, fmt.Errorf("unauthorized")
	}

	args, ok := request.Params.Arguments.(map[string]any)
	if !ok {
		args = make(map[string]any)
	}

	var tag string
	if tagVal, exists := args["tag"]; exists {
		if t, ok := tagVal.(string); ok {
			tag = t
		}
	}

	page := 1
	if pageVal, exists := args["page"]; exists {
		if value, ok := pageVal.(float64); ok {
			page = int(value)
		}
	}
	if page <= 0 {
		return nil, fmt.Errorf("page must be a positive integer")
	}

	pageSize := 20
	if pageSizeVal, exists := args["page_size"]; exists {
		if value, ok := pageSizeVal.(float64); ok {
			pageSize = int(value)
		}
	}
	if pageSize <= 0 || pageSize > 100 {
		return nil, fmt.Errorf("page_size must be between 1 and 100")
	}

	files, err := h.fileUsecase.ListPaginated(ctx, dbUser.ID, tag, page, pageSize)
	if err != nil {
		slog.Error("MCP list_files tool failed", "user_id", dbUser.ID, "error", err)
		return nil, fmt.Errorf("failed to list files: %w", err)
	}

	data, err := json.Marshal(files)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize files list: %w", err)
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.NewTextContent(string(data)),
		},
	}, nil
}

func (h *MCPHandler) handleSearchFiles(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	dbUser, ok := ctx.Value(userCtxKey).(*user.User)
	if !ok {
		slog.Warn("MCP tool search_files invoked without authenticated user context")
		return nil, fmt.Errorf("unauthorized")
	}

	args, ok := request.Params.Arguments.(map[string]any)
	if !ok {
		args = make(map[string]any)
	}

	queryVal, ok := args["query"]
	if !ok {
		return nil, fmt.Errorf("missing query parameter")
	}
	query, ok := queryVal.(string)
	if !ok {
		return nil, fmt.Errorf("invalid query parameter type")
	}

	limit := 5
	if limitVal, exists := args["limit"]; exists {
		if flLimit, ok := limitVal.(float64); ok {
			limit = int(flLimit)
		}
	}

	nlpExpansion := false
	if nlpVal, exists := args["nlp_expansion"]; exists {
		if val, ok := nlpVal.(bool); ok {
			nlpExpansion = val
		}
	}

	expansionNum := 3
	if expVal, exists := args["expansion_num"]; exists {
		if val, ok := expVal.(float64); ok {
			expansionNum = int(val)
		}
	}

	items, err := h.fileUsecase.Search(ctx, dbUser.ID, query, limit, nlpExpansion, expansionNum)
	if err != nil {
		slog.Error("MCP search_files tool failed", "user_id", dbUser.ID, "query", query, "error", err)
		return nil, fmt.Errorf("failed to perform search: %w", err)
	}

	data, err := json.Marshal(items)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize search results: %w", err)
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.NewTextContent(string(data)),
		},
	}, nil
}

func (h *MCPHandler) handleReadFile(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	dbUser, ok := ctx.Value(userCtxKey).(*user.User)
	if !ok {
		slog.Warn("MCP tool read_file invoked without authenticated user context")
		return nil, fmt.Errorf("unauthorized")
	}

	args, ok := request.Params.Arguments.(map[string]any)
	if !ok {
		args = make(map[string]any)
	}

	fileIDVal, ok := args["file_id"]
	if !ok {
		return nil, fmt.Errorf("missing file_id parameter")
	}
	fileID, ok := fileIDVal.(string)
	if !ok {
		return nil, fmt.Errorf("invalid file_id parameter type")
	}

	text, err := h.fileUsecase.ExtractText(ctx, dbUser.ID, fileID)
	if err != nil {
		slog.Error("MCP read_file tool failed to extract text", "user_id", dbUser.ID, "file_id", fileID, "error", err)
		return nil, fmt.Errorf("failed to extract text: %w", err)
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.NewTextContent(text),
		},
	}, nil
}

// StreamableHTTP handles MCP Streamable HTTP requests.
// @Summary      MCP Server (Streamable HTTP)
// @Description  Handles MCP Streamable HTTP requests over a single /mcp endpoint.
// @Tags         mcp
// @Accept       json
// @Produce      json
// @Success      200 {object} StreamResponse "MCP response"
// @Security     BasicAuth
// @Router       /mcp [get]
// @Router       /mcp [post]
// @Router       /mcp [delete]
func (h *MCPHandler) StreamableHTTP(c *gin.Context) {
	gin.WrapH(h.StreamableHTTPServer)(c)
}
