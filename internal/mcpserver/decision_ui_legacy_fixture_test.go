package mcpserver

import _ "embed"

// Archived behavior fixture only. The production binary neither embeds nor
// registers this superseded standalone decision surface.
//
//go:embed decision_ui.html
var decisionUIHTML string
