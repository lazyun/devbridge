package mcpserver

import (
	_ "embed"
	"encoding/json"
	"strings"
)

//go:embed confirmation_stack_runtime.js
var confirmationStackRuntime string

//go:embed confirmation_stack.css
var confirmationStackCSS string

func confirmationStackHTML(base string) string {
	section := func(start, end string) string {
		_, s, ok := strings.Cut(confirmationHTML, start)
		if !ok {
			panic("missing formal Card section")
		}
		v, _, ok := strings.Cut(s, end)
		if !ok {
			panic("missing formal Card boundary")
		}
		return v
	}
	main := section(`<script id="confirmation-main">`, `</script>`)
	css := section(`<style id="confirmation-style">`, `</style>`)
	css = strings.ReplaceAll(css, ":root", ":host")
	css = strings.ReplaceAll(css, "body.dbc-surface", ".dbc-surface")
	css += "\n" + confirmationStackCSS
	body := section(`<body class="dbc-surface">`, `<script id="confirmation-main">`)
	encode := func(v string) string { b, _ := json.Marshal(v); return string(b) }
	script := strings.NewReplacer("__CONFIRMATION_CSS__", encode(css), "__CONFIRMATION_BODY__", encode(body), "__CONFIRMATION_SOURCE__", encode(main), "__CONFIRMATION_MAIN__", main).Replace(confirmationStackRuntime)
	return strings.Replace(base, "</head>", "<script id=\"confirmation-stack\">"+script+"</script>\n</head>", 1)
}
