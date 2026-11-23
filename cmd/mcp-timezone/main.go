// Command mcp-timezone is Nestor's reference MCP server: two read-only tools that tell the time in
// a time zone and convert a time between zones. It needs no credentials and no network access, and
// carries its own time zone data, so it runs from an empty image on an isolated network.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"
	_ "time/tzdata" // the image has no /usr/share/zoneinfo

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type getTimeArgs struct {
	Timezone string `json:"timezone" jsonschema:"IANA time zone name, such as Europe/Zurich"`
}

type convertTimeArgs struct {
	Time         string `json:"time" jsonschema:"time of day as HH:MM (24-hour), read on today's date in from_timezone"`
	FromTimezone string `json:"from_timezone" jsonschema:"IANA time zone the time is given in"`
	ToTimezone   string `json:"to_timezone" jsonschema:"IANA time zone to convert to"`
}

// newServer returns the server, reading the clock through now.
func newServer(now func() time.Time) *sdk.Server {
	s := sdk.NewServer(&sdk.Implementation{Name: "nestor-timezone", Version: "1.0.0"}, nil)
	readOnly := &sdk.ToolAnnotations{ReadOnlyHint: true}

	sdk.AddTool(s, &sdk.Tool{
		Name:        "get_time",
		Description: "Returns the current date and time in an IANA time zone.",
		Annotations: readOnly,
	}, func(_ context.Context, _ *sdk.CallToolRequest, in getTimeArgs) (*sdk.CallToolResult, any, error) {
		loc, err := zone(in.Timezone)
		if err != nil {
			return failure(err), nil, nil
		}
		t := now().In(loc)
		return text(fmt.Sprintf("%s (%s, UTC%s)", t.Format("2006-01-02 15:04"), loc, t.Format("-07:00"))), nil, nil
	})

	sdk.AddTool(s, &sdk.Tool{
		Name:        "convert_time",
		Description: "Converts a time of day today from one IANA time zone to another.",
		Annotations: readOnly,
	}, func(_ context.Context, _ *sdk.CallToolRequest, in convertTimeArgs) (*sdk.CallToolResult, any, error) {
		from, err := zone(in.FromTimezone)
		if err != nil {
			return failure(err), nil, nil
		}
		to, err := zone(in.ToTimezone)
		if err != nil {
			return failure(err), nil, nil
		}
		clock, err := time.Parse("15:04", in.Time)
		if err != nil {
			return failure(errors.New("time must be HH:MM, such as 09:30")), nil, nil
		}
		today := now().In(from)
		at := time.Date(today.Year(), today.Month(), today.Day(), clock.Hour(), clock.Minute(), 0, 0, from)
		return text(fmt.Sprintf("%s in %s is %s in %s", at.Format("15:04"), from, at.In(to).Format("15:04"), to)), nil, nil
	})
	return s
}

func zone(name string) (*time.Location, error) {
	if name == "" || name == "Local" {
		return nil, errors.New("unknown time zone: give an IANA name such as Europe/Zurich")
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("unknown time zone %q", name)
	}
	return loc, nil
}

func text(s string) *sdk.CallToolResult {
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: s}}}
}

func failure(err error) *sdk.CallToolResult {
	return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: err.Error()}}}
}

func main() {
	addr := flag.String("addr", ":8080", "address to serve streamable HTTP on, at /mcp")
	flag.Parse()

	server := newServer(time.Now)
	mux := http.NewServeMux()
	mux.Handle("/mcp", sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil))
	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	slog.Info("mcp-timezone listening", "addr", *addr)
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("mcp-timezone stopped", "error", err)
		os.Exit(1)
	}
}
