package wire

type InitializeParams struct {
	ClientInfo   ClientInfo              `json:"clientInfo"`
	Capabilities *InitializeCapabilities `json:"capabilities,omitempty"`
}

type InitializeCapabilities struct {
	ExperimentalAPI bool `json:"experimentalApi"`
}

type ClientInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
}

type InitializeResponse struct {
	UserAgent      string `json:"userAgent"`
	PlatformFamily string `json:"platformFamily"`
	PlatformOS     string `json:"platformOs"`
	CodexHome      string `json:"codexHome"`
}
