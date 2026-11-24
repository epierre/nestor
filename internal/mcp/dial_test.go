package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestDialHTTPWithHeader(t *testing.T) {
	tests := []struct {
		name        string
		headerName  string
		headerValue string
		wantAuth    string
		wantAPIKey  string
	}{
		{
			name:        "custom header without Authorization",
			headerName:  "X-Goog-Api-Key",
			headerValue: "test-maps-key",
			wantAuth:    "",
			wantAPIKey:  "test-maps-key",
		},
		{
			name:        "bearer header",
			headerName:  "Authorization",
			headerValue: "Bearer test-token",
			wantAuth:    "Bearer test-token",
			wantAPIKey:  "",
		},
		{
			name:        "empty header",
			headerName:  "",
			headerValue: "",
			wantAuth:    "",
			wantAPIKey:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := sdk.NewServer(&sdk.Implementation{Name: "maps", Version: "0"}, nil)
			srv.AddTool(&sdk.Tool{Name: "search_places", Description: "Search places.", InputSchema: map[string]any{"type": "object"}},
				func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
					return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "ok"}}}, nil
				})

			var mu sync.Mutex
			var seenAuth, seenKey []string
			handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return srv }, nil)
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				seenAuth = append(seenAuth, r.Header.Get("Authorization"))
				seenKey = append(seenKey, r.Header.Get("X-Goog-Api-Key"))
				mu.Unlock()
				handler.ServeHTTP(w, r)
			}))
			t.Cleanup(ts.Close)

			s, err := DialHTTPWithHeader(context.Background(), ts.URL+"/mcp", tt.headerName, tt.headerValue)
			if err != nil {
				t.Fatalf("DialHTTPWithHeader: %v", err)
			}
			defer s.Close()

			tools, err := s.ListTools(context.Background())
			if err != nil {
				t.Fatalf("ListTools: %v", err)
			}
			if len(tools) != 1 || tools[0].Name != "search_places" {
				t.Fatalf("ListTools = %+v, want [search_places]", tools)
			}
			res, err := s.CallTool(context.Background(), "search_places", json.RawMessage(`{}`))
			if err != nil {
				t.Fatalf("CallTool: %v", err)
			}
			if resultText(res) != "ok" {
				t.Fatalf("CallTool text = %q, want ok", resultText(res))
			}

			mu.Lock()
			defer mu.Unlock()
			if len(seenAuth) == 0 {
				t.Fatal("server saw zero HTTP requests")
			}
			for i := range seenAuth {
				if seenAuth[i] != tt.wantAuth {
					t.Errorf("request %d Authorization = %q, want %q", i, seenAuth[i], tt.wantAuth)
				}
				if seenKey[i] != tt.wantAPIKey {
					t.Errorf("request %d X-Goog-Api-Key = %q, want %q", i, seenKey[i], tt.wantAPIKey)
				}
			}
		})
	}

	t.Run("error does not echo header secret", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		}))
		ts.Close() // closed immediately to force dial failure, or test 401 below

		ts401 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		}))
		t.Cleanup(ts401.Close)

		for _, u := range []string{ts.URL + "/mcp", ts401.URL + "/mcp"} {
			_, err := DialHTTPWithHeader(context.Background(), u, "X-Goog-Api-Key", "test-maps-key")
			if err == nil {
				t.Fatalf("DialHTTPWithHeader(%s): expected error, got nil", u)
			}
			if strings.Contains(err.Error(), "test-maps-key") {
				t.Fatalf("error echoed secret value: %v", err)
			}
		}
	})
}
