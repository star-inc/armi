package http

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/gin-gonic/gin"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/supersonictw/armi/internal/usecase"
	"github.com/supersonictw/armi/pkgs/user"
)

// MCPHandler handles Model Context Protocol requests over SSE.
type MCPHandler struct {
	fileUsecase *usecase.FileUsecase
	SSEServer   *server.SSEServer
}

// NewMCPHandler initializes the MCP server and registers tools.
func NewMCPHandler(fileUsecase *usecase.FileUsecase) *MCPHandler {
	mcpServer := server.NewMCPServer("armi-mcp-server", "1.0.0")

	h := &MCPHandler{
		fileUsecase: fileUsecase,
	}

	// 1. Register list_files tool
	listFilesTool := mcp.NewTool("list_files",
		mcp.WithDescription("列出當前使用者上傳的所有檔案清單"),
		mcp.WithString("tag", mcp.Description("過濾特定的檔案標籤（選填）")),
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

	// Wrap in SSE server. Messages will be posted to /api/v1/mcp/message
	sseServer := server.NewSSEServer(
		mcpServer,
		server.WithSSEEndpoint("/api/v1/mcp"),
		server.WithMessageEndpoint("/api/v1/mcp/message"),
	)
	h.SSEServer = sseServer

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

	files, err := h.fileUsecase.List(ctx, dbUser.ID, tag)
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

// SSEConnect handles the SSE connection handshake for the MCP server.
// @Summary      Connect to MCP Server (SSE)
// @Description  Establishes a Streamable HTTP (SSE) connection channel.
// @Tags         mcp
// @Produce      text/event-stream
// @Success      200 {string} string "SSE Connection established"
// @Security     BasicAuth
// @Router       /mcp [get]
func (h *MCPHandler) SSEConnect(c *gin.Context) {
	gin.WrapH(h.SSEServer.SSEHandler())(c)
}

// ReceiveMessage handles JSON-RPC 2.0 messages from the client.
// @Summary      Post message to MCP Server
// @Description  Sends JSON-RPC 2.0 request messages (like initialize, tools/list, tools/call).
// @Tags         mcp
// @Accept       json
// @Produce      json
// @Param        sessionId query string true "Session ID (SSE Client)"
// @Param        request body string true "JSON-RPC Request"
// @Success      200 {string} string "Message accepted"
// @Security     BasicAuth
// @Router       /mcp/message [post]
func (h *MCPHandler) ReceiveMessage(c *gin.Context) {
	gin.WrapH(h.SSEServer.MessageHandler())(c)
}
