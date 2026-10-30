package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/lazyun/devbridge/internal/codexruntime"
)

type runtimeInventorySnapshot struct {
	Revision                uint64                                `json:"revision"`
	CodingAgentModels       []codexruntime.CodingAgentModelConfig `json:"codingAgentModels"`
	AllowedModels           []string                              `json:"allowedModels"`
	AllowedReasoningEfforts []string                              `json:"allowedReasoningEfforts"`
}

type runtimeInventoryCoreAPI interface {
	Get(context.Context, string) (runtimeInventorySnapshot, error)
	Apply(context.Context, string, runtimeInventorySnapshot) (runtimeInventorySnapshot, error)
}

type httpRuntimeInventoryCoreAPI struct{ client *http.Client }

func newHTTPRuntimeInventoryCoreAPI(client *http.Client) runtimeInventoryCoreAPI {
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	return &httpRuntimeInventoryCoreAPI{client: client}
}

func (c *httpRuntimeInventoryCoreAPI) Get(ctx context.Context, endpoint string) (runtimeInventorySnapshot, error) {
	target, err := coreURL(endpoint, "/v1/runtime/codex-inventory")
	if err != nil {
		return runtimeInventorySnapshot{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return runtimeInventorySnapshot{}, err
	}
	return c.do(request)
}

func (c *httpRuntimeInventoryCoreAPI) Apply(ctx context.Context, endpoint string, value runtimeInventorySnapshot) (runtimeInventorySnapshot, error) {
	payload, err := json.Marshal(map[string]any{
		"expectedRevision":  value.Revision,
		"codingAgentModels": value.CodingAgentModels,
	})
	if err != nil {
		return runtimeInventorySnapshot{}, err
	}
	target, err := coreURL(endpoint, "/v1/runtime/codex-inventory")
	if err != nil {
		return runtimeInventorySnapshot{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, target, bytes.NewReader(payload))
	if err != nil {
		return runtimeInventorySnapshot{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	return c.do(request)
}

func (c *httpRuntimeInventoryCoreAPI) do(request *http.Request) (runtimeInventorySnapshot, error) {
	response, err := c.client.Do(request)
	if err != nil {
		return runtimeInventorySnapshot{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return runtimeInventorySnapshot{}, decodeCoreError(response.Body, response.StatusCode)
	}
	var value runtimeInventorySnapshot
	if err := decodeCoreJSON(response.Body, &value); err != nil {
		return runtimeInventorySnapshot{}, err
	}
	if value.Revision == 0 || len(value.CodingAgentModels) == 0 {
		return runtimeInventorySnapshot{}, errors.New("Core returned an invalid Codex runtime inventory snapshot")
	}
	return value, nil
}
