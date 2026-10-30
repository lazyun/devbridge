package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestConsoleRespondsUsesHTTPStatusOnly(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       bool
	}{
		{"production title", `<title>Dev Bridge</title>`, 200, true},
		{"arbitrary title", `<title>Completely Different Product Text</title>`, 200, true},
		{"empty body", "", 200, true},
		{"missing console", "", 404, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); io.WriteString(w, tc.body) }))
			defer server.Close()
			got, err := consoleResponds(context.Background(), server.URL+"/ui/")
			if err != nil || got != tc.want {
				t.Fatalf("consoleResponds = %v, %v", got, err)
			}
		})
	}
}
