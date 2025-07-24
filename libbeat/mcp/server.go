package mcp

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/elastic/elastic-agent-libs/logp"
)

//go:embed prompts/health_check_assistant.md
var promptHealthCheckAssistant string

const uriScheme = "beats://"

type Config struct {
	Enabled bool `config:"enabled"`
	Port    int  `config:"port"`
}

type Server struct {
	s   *mcp.Server
	log *logp.Logger
}

func New(beat string, config *Config, logger *logp.Logger) *Server {
	log := logger.Named("mcp").With("anderson", "AndersonQ")
	server := mcp.NewServer(
		&mcp.Implementation{
			Name:    beat + "-mcp",
			Title:   beat + "'s MCP server",
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

	prompt := mcp.Prompt{
		Name:        "ingest_files_ok",
		Title:       "Filebeat file ingestion",
		Description: "Check if filebeat is ingesting files",
		Arguments:   nil,
	}
	server.AddPrompt(&prompt,
		func(ctx context.Context,
			session *mcp.ServerSession, params *mcp.GetPromptParams) (*mcp.GetPromptResult, error) {
			return &mcp.GetPromptResult{
				Description: "Is filebeat running ",
				Messages: []*mcp.PromptMessage{
					{
						Role: "user",
						Content: &mcp.TextContent{
							Text: "is everything ok with " + beat + "?"},
					}, {
						Role: "assistant",
						Content: &mcp.TextContent{
							Text: promptHealthCheckAssistant},
					},
				},
			}, nil

		})

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

	return s
}

type toolOut struct {
	Output string `json:"output"`
}

func (s Server) AddTool(
	name,
	title,
	description string,
	do func() (string, bool)) Server {
	mcp.AddTool(s.s,
		&mcp.Tool{
			Name:        name,
			Title:       title,
			Description: description,
		},
		func(ctx context.Context, session *mcp.ServerSession, c *mcp.CallToolParamsFor[struct{}]) (*mcp.CallToolResultFor[toolOut], error) {
			out, err := do()

			return &mcp.CallToolResultFor[toolOut]{
				Content: []mcp.Content{&mcp.TextContent{
					Text: fmt.Sprintf(`{"output":%q}`, out),
				}},
				StructuredContent: toolOut{Output: fmt.Sprintf("%q", out)},
				IsError:           err,
			}, nil
		})

	s.log.Infof("added tool %s", name)
	return s
}

type toolInLogsByDay struct {
	// IncludeEventDataLogs determines if the logs from the 'event_data' logger
	// should be included in the response. The 'event_data' logger logs include
	// raw events rejected by the output. It might contain sensitive data.
	IncludeEventDataLogs bool
	// Data is the date to fetch logs for in the format YYYYMMDD.
	Date string
}

type toolOutLogsByDay struct {
	// Logs are the beat logs
	Logs string `json:"logs"`
	// EventDataLogs is Filebeat's 'event_data' logger logs, which may contain
	// raw events and therefore, sensitive data.
	EventDataLogs string `json:"event_data_logs,omitempty"`
	// Error is the error message if the tool encounters an error.
	Error string `json:"error,omitempty"`
}

func (s Server) AddToolLogsByDay(beat string, getLogFilePaths func() ([]string, error)) Server {
	name := beat + "-logs-by-day"
	mcp.AddTool(s.s,
		&mcp.Tool{
			Name:        name,
			Title:       "Fetches " + beat + " logs for a day",
			Description: "Fetches all " + beat + " filebeat logs fro a given day",
		},
		func(ctx context.Context, session *mcp.ServerSession, callParams *mcp.CallToolParamsFor[toolInLogsByDay]) (*mcp.CallToolResultFor[toolOutLogsByDay], error) {
			args := callParams.Arguments
			paths, err := getLogFilePaths()
			if err != nil {
				msg := fmt.Sprintf("could not get log files: %v", err)
				return &mcp.CallToolResultFor[toolOutLogsByDay]{
					Content: []mcp.Content{&mcp.TextContent{
						Text: fmt.Sprintf(`{"output":%q}`, msg),
					}},
					StructuredContent: toolOutLogsByDay{Error: fmt.Sprintf("%q", msg)},
					IsError:           true,
				}, nil
			}

			// Split into logs and event logs and filter for date
			var regLogs, eventLogs []string
			for _, path := range paths {
				if !strings.Contains(path, args.Date) {
					continue
				}

				if strings.Contains(path, "event_data") {
					if args.IncludeEventDataLogs {
						eventLogs = append(eventLogs, path)
					} else {
						continue
					}
				}

				regLogs = append(regLogs, path)
			}
			s.log.Infof("AddToolLogsByDay, regLogs: %v, eventLogs: %v",
				regLogs, eventLogs)

			readLogs := func(logs []string) []byte {
				var allLogs []byte
				for _, logPath := range regLogs {
					data, err := os.ReadFile(logPath)
					if err != nil {
						data = []byte(fmt.Sprintf(
							"ERROR: could not read log file %s: %v", logPath, err))
					}

					allLogs = append(allLogs, data...)
				}

				return allLogs
			}

			regContents := readLogs(regLogs)
			out := toolOutLogsByDay{
				Logs: string(regContents),
			}
			if args.IncludeEventDataLogs {
				out.EventDataLogs = string(readLogs(eventLogs))
			}

			unstructured, err := json.Marshal(out)
			if err != nil {
				unstructured = []byte(fmt.Sprintf(
					"ERROR: could not marshal tool outptu: %v", err))
			}

			// Add the contents to the result
			return &mcp.CallToolResultFor[toolOutLogsByDay]{
				Content: []mcp.Content{&mcp.TextContent{
					Text: string(unstructured)}},
				StructuredContent: out,
				IsError:           false,
			}, nil
		})

	s.log.Infof("added tool %s", name)
	return s
}

func (s Server) AddResourceLogFiles(beat string, getFiles func() ([]string, error)) Server {

	newHandler := func(uri string, keepLog func(path string) bool) func(ctx context.Context,
		session *mcp.ServerSession, params *mcp.ReadResourceParams) (*mcp.ReadResourceResult, error) {

		return func(ctx context.Context,
			session *mcp.ServerSession, params *mcp.ReadResourceParams) (*mcp.ReadResourceResult, error) {
			var contents []*mcp.ResourceContents

			fs, err := getFiles()
			if err != nil {
				s.log.Errorf("AddResourceLogFiles: could not get files: %v", err)
				return &mcp.ReadResourceResult{
					Contents: []*mcp.ResourceContents{{
						URI:      uri,
						MIMEType: "text/plain",
						Text:     fmt.Sprintf("error getting files: %v", err),
					}},
				}, nil
			}

			// TODO(AndersonQ): handle event log files: "event-data"
			s.log.Infof("adding files %s", fs)
			for _, f := range fs {
				if !keepLog(f) {
					continue
				}

				content := &mcp.ResourceContents{
					URI:      uri + "/" + f,
					MIMEType: "application/json-seq",
				}
				data, err := os.ReadFile(f)
				if err != nil {
					content.MIMEType = "text/plain"
					content.Text = fmt.Sprintf("error reading file: %v", err)
					contents = append(contents, content)
					continue
				}
				if len(data) == 0 {
					continue
				}
				content.Text = string(data)

				contents = append(contents, content)
			}

			if len(contents) == 0 {
				contents = append(contents, &mcp.ResourceContents{
					URI:      uri + "/no_logs",
					MIMEType: "text/plain",
					Text:     "no log files found",
				})
			}

			s.log.Infof("returning contents: %#v", contents)

			return &mcp.ReadResourceResult{
				Contents: contents,
			}, nil
		}
	}

	uri := uriScheme + beat + "/logs"
	keepLogs := func(path string) bool {
		return !strings.Contains(path, "event_data")
	}
	r := &mcp.Resource{
		Name:        beat + "/logs",
		Title:       beat + " logs",
		Description: "All current log files for " + beat + ". Each log file is one content",
		MIMEType:    "application/json-seq",
		URI:         uri,
	}
	s.s.AddResource(r, newHandler(uri, keepLogs))
	s.log.Infof("added log resource %s", uri)

	uri = uriScheme + beat + "/logs/event_data"
	keepLogs = func(path string) bool {
		return strings.Contains(path, "event_data")
	}
	r = &mcp.Resource{
		Name:  beat + "/logs/event_data",
		Title: beat + " event data logs",
		Description: "All current event data log files for " + beat + ". The " +
			"event data log files might contain raw events and therefore " +
			"sensitive data. Each log file is one content",
		MIMEType: "application/json-seq",
		URI:      uri,
	}
	s.s.AddResource(r, newHandler(uri, keepLogs))
	s.log.Infof("added log resource %s", uri)

	return s
}
