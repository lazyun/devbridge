package mcpserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"

	"github.com/lazyun/devbridge/internal/controlapi"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Panel focus is ephemeral adapter UI state, never a Core work selection,
// authorization or workflow transition. Every read/write still checks Session
// ownership and the exact Project/Area/Task through the normal Core readers.
type panelNavigation struct {
	AISessionID   string `json:"aiSessionId"`
	ProjectID     string `json:"projectId"`
	FeatureAreaID string `json:"featureAreaId"`
	TaskID        string `json:"taskId"`
	Epoch         string `json:"epoch"`
	Version       int64  `json:"version"`
}

type panelNavigationStore struct {
	mu      sync.Mutex
	epoch   string
	version int64
	values  map[string]panelNavigation
}

func newPanelNavigationStore() *panelNavigationStore {
	var epoch [16]byte
	if _, err := rand.Read(epoch[:]); err != nil {
		panic(err)
	}
	return &panelNavigationStore{epoch: hex.EncodeToString(epoch[:]), values: make(map[string]panelNavigation)}
}

func (s *panelNavigationStore) get(session, project string) *panelNavigation {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.values[session]
	if !ok || v.ProjectID != project {
		return nil
	}
	return &v
}

func (s *panelNavigationStore) set(v panelNavigation) panelNavigation {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.values[v.AISessionID]; ok && old.ProjectID == v.ProjectID && old.FeatureAreaID == v.FeatureAreaID && old.TaskID == v.TaskID {
		return old
	}
	// Bound adapter memory without adding presentation state to business tables.
	if len(s.values) >= 1024 {
		oldestID := ""
		var oldest int64 = 1<<63 - 1
		for id, old := range s.values {
			if old.Version < oldest {
				oldestID, oldest = id, old.Version
			}
		}
		delete(s.values, oldestID)
	}
	s.version++
	v.Epoch, v.Version = s.epoch, s.version
	s.values[v.AISessionID] = v
	return v
}

type panelFeedOutput struct {
	controlapi.PanelConfirmationFeed
	Navigation *panelNavigation `json:"navigation,omitempty"`
}

type panelFocusInput struct {
	AISessionID   string `json:"aiSessionId"`
	ProjectID     string `json:"projectId"`
	FeatureAreaID string `json:"featureAreaId,omitempty"`
	TaskID        string `json:"taskId,omitempty"`
}

type panelFocusOutput struct {
	Navigation panelNavigation `json:"navigation"`
	Guarantee  string          `json:"guarantee"`
}

func registerPanelFocus(server *sdkmcp.Server, service BridgeService, store *panelNavigationStore) {
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "panel_focus", Title: "Switch existing panel target", Description: "SILENT UI-only navigation of this conversation's existing Dev Bridge side panel. After the user asks to view or continue an existing Task, resolve the exact Task with work_list/workspace_get and call this with the same real aiSessionId and bound projectId; taskId derives its actual FeatureArea, and a supplied featureAreaId is an assertion. Omit Task to show an explicit Area overview; omit both to show Project overview. Ambiguous names require user selection. The existing panel polls this focus; do not remount panel_show or emit another Card. This changes no Core business records, prepares no confirmation, grants no authority and does not continue/start/recover execution. No cross-Project rebinding. Focus is ephemeral across server restarts; failure retains the previous target.", InputSchema: json.RawMessage(`{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"projectId":{"type":"string","minLength":1,"maxLength":128},"featureAreaId":{"type":"string","minLength":1,"maxLength":128},"taskId":{"type":"string","minLength":1,"maxLength":128}},"required":["aiSessionId","projectId"],"additionalProperties":false}`), Annotations: toolAnnotations("Switch panel display only", false, true)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in panelFocusInput) (*sdkmcp.CallToolResult, panelFocusOutput, error) {
		identity, err := validatePresentationIdentity(ctx, service, devBridgeCardInput{AISessionID: in.AISessionID, ProjectID: in.ProjectID, FeatureAreaID: in.FeatureAreaID, TaskID: in.TaskID})
		if err != nil {
			return nil, panelFocusOutput{}, err
		}
		identity.SelectionMode = "EXACT"
		w, err := devBridgeCardProjection(ctx, service, identity)
		if err != nil {
			return nil, panelFocusOutput{}, err
		}
		if w.DevelopmentContext.Project.ID != identity.ProjectID || w.SelectedFeatureAreaID != identity.FeatureAreaID || w.SelectedTaskID != identity.TaskID || w.ReadError != "" {
			return nil, panelFocusOutput{}, errors.New("PANEL_TARGET_UNAVAILABLE: exact display target could not be read")
		}
		v := store.set(panelNavigation{AISessionID: in.AISessionID, ProjectID: identity.ProjectID, FeatureAreaID: identity.FeatureAreaID, TaskID: identity.TaskID})
		return &sdkmcp.CallToolResult{Meta: sdkmcp.Meta{"emission": "SILENT"}}, panelFocusOutput{Navigation: v, Guarantee: "DISPLAY_ONLY_NO_BUSINESS_CHANGE"}, nil
	})
}
