package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/elastic/elastic-agent-libs/logp"
)

const uriScheme = "beats://"

type Config struct {
	Enabled bool `config:"enabled"`
	Port    int  `config:"port"`
}

type Server struct {
	s   *mcp.Server
	log *logp.Logger
}

func New(name string, config *Config, logger *logp.Logger) *Server {
	log := logger.Named("mcp").With("anderson", "AndersonQ")
	server := mcp.NewServer(
		&mcp.Implementation{
			Name:    name + "-mcp",
			Title:   name + "'s MCP server",
			Version: "v0.0.1"},
		nil)

	// if we're here, it's because the MCP server is enabled
	port := config.Port
	if port == 0 {
		port = 4242
	}

	httpServer := http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: nil,
	}

	// TODO(AndersonQ): add a shutdown
	h := mcp.NewSSEHandler(func(_ *http.Request) *mcp.Server { return server })
	httpServer.Handler = h
	go func() {
		err := httpServer.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Errorf("MCP HTTP server ListenAndServe error : %v", err)
		}
	}()
	log.Infof("AndersonQ: MCP server running on %s", httpServer.Addr)

	return &Server{
		s:   server,
		log: log,
	}
}

func (s Server) AddResource(
	name,
	title,
	description,
	mimeType string,
	gerResource func() string) Server {

	uri := uriScheme + name
	r := &mcp.Resource{
		Name:        name,
		Title:       title,
		Description: description,
		MIMEType:    mimeType,
		URI:         uri,
	}
	rHandler := func(ctx context.Context, session *mcp.ServerSession, params *mcp.ReadResourceParams) (*mcp.ReadResourceResult, error) {

		text := gerResource()
		if text == "" {
			text = "no data"
		}
		resp := &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{{
				URI:      uri,
				MIMEType: mimeType,
				Text:     text,
			}},
		}
		return resp, nil
	}
	s.s.AddResource(r, rHandler)
	s.log.Infof("added resource %s", name)

	toolName := "fallback-tool-" + name
	toolName = strings.ReplaceAll(toolName, "/", "_")
	toolName = strings.ReplaceAll(toolName, ".", "_")
	mcp.AddTool(s.s,
		&mcp.Tool{
			Name:  toolName,
			Title: fmt.Sprintf("fallback tool for resource \"%s\"", title),
			Description: "This tool is a fallback for applications which do not support resources. The same content is available as a resource at " + uri + ".\n" +
				description,
		},
		func(ctx context.Context, session *mcp.ServerSession, c *mcp.CallToolParamsFor[struct{}]) (*mcp.CallToolResultFor[struct{}], error) {
			return &mcp.CallToolResultFor[struct{}]{
				Content: []mcp.Content{&mcp.TextContent{
					Text: gerResource(),
				}},
			}, nil
		})

	s.log.Infof("added tool %s", toolName)

	return s
}
