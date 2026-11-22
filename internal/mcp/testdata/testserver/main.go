// Command testserver is a minimal MCP server for integration tests. It serves over stdio by
// default, or over streamable HTTP at /mcp when started with -http <addr>.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type noArgs struct{}

func newServer() *sdk.Server {
	s := sdk.NewServer(&sdk.Implementation{Name: "testserver", Version: "0.0.1"}, nil)
	for _, name := range []string{"get_forecast", "get_tasks"} {
		sdk.AddTool(s, &sdk.Tool{Name: name, Description: "test tool " + name},
			func(ctx context.Context, req *sdk.CallToolRequest, in noArgs) (*sdk.CallToolResult, any, error) {
				return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "ok"}}}, nil, nil
			})
	}
	return s
}

func main() {
	addr := flag.String("http", "", "serve streamable HTTP on this address instead of stdio")
	flag.Parse()

	if *addr == "" {
		if err := newServer().Run(context.Background(), &sdk.StdioTransport{}); err != nil {
			log.Fatal(err)
		}
		return
	}

	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return newServer() }, nil)
	mux := http.NewServeMux()
	mux.Handle("/mcp", handler)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}
