package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"testing"
	"time"
)

func modelCatalogClient(t *testing.T, pages []string) *Client {
	t.Helper()
	stdoutReader, stdoutWriter := io.Pipe()
	stdinReader, stdinWriter := io.Pipe()
	client := &Client{}
	client.rpc = newRPCTransport(stdoutReader, stdinWriter, nil)
	t.Cleanup(func() { client.rpc.Close(); stdinReader.Close(); stdoutWriter.Close() })
	go func() {
		requests := bufio.NewReader(stdinReader)
		for i, page := range pages {
			line, err := requests.ReadBytes('\n')
			if err != nil {
				return
			}
			var request struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params struct {
					Cursor        *string `json:"cursor"`
					IncludeHidden bool    `json:"includeHidden"`
				} `json:"params"`
			}
			if err := json.Unmarshal(line, &request); err != nil {
				t.Error(err)
				return
			}
			if request.Method != "model/list" || request.Params.IncludeHidden {
				t.Errorf("unexpected request: %s", line)
				return
			}
			if i == 0 && request.Params.Cursor != nil {
				t.Errorf("first cursor must be null: %s", line)
				return
			}
			if i > 0 && (request.Params.Cursor == nil || *request.Params.Cursor != "next") {
				t.Errorf("pagination cursor missing: %s", line)
				return
			}
			if _, err := fmt.Fprintf(stdoutWriter, "{\"id\":%s,\"result\":%s}\n", request.ID, page); err != nil {
				return
			}
		}
	}()
	return client
}

const testCatalogModel = `{"id":"picker-id","model":"gpt-new-model","defaultReasoningEffort":"high","supportedReasoningEfforts":[{"reasoningEffort":"low"},{"reasoningEffort":"high"}],"serviceTiers":[{"id":"priority"}]}`

func TestListModelsUsesWireModelAndReadsAllPages(t *testing.T) {
	client := modelCatalogClient(t, []string{
		`{"data":[` + testCatalogModel + `,{"model":"hidden","hidden":true}],"nextCursor":"next"}`,
		`{"data":[` + testCatalogModel + `,{"model":"gpt-other","defaultReasoningEffort":"future","supportedReasoningEfforts":[{"reasoningEffort":"future"}]}]}`,
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	models, err := client.ListModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ModelID != "gpt-new-model" || models[0].Enabled || models[0].DefaultReasoningEffort != "high" || models[0].FastTier() != "priority" {
		t.Fatalf("models=%+v", models)
	}
	if models[1].FastServiceTier == nil || models[1].FastTier() != "" {
		t.Fatal("unsupported Fast tier was inferred")
	}
}

func TestListModelsRejectsEmptyMalformedAndRepeatedPages(t *testing.T) {
	for name, pages := range map[string][]string{
		"empty":           {`{"data":[]}`},
		"bad default":     {`{"data":[{"model":"gpt-invalid","defaultReasoningEffort":"ultra","supportedReasoningEfforts":[{"reasoningEffort":"low"}]}]}`},
		"repeated cursor": {`{"data":[` + testCatalogModel + `],"nextCursor":"next"}`, `{"data":[],"nextCursor":"next"}`},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			models, err := modelCatalogClient(t, pages).ListModels(ctx)
			if err == nil || models != nil {
				t.Fatalf("partial/invalid catalog accepted: models=%+v err=%v", models, err)
			}
		})
	}
}

func TestListModelsHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := modelCatalogClient(t, nil).ListModels(ctx); err == nil {
		t.Fatal("cancelled model read succeeded")
	}
}
