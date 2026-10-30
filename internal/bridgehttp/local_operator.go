package bridgehttp

import (
	"net/http"
	"strings"

	"github.com/lazyun/devbridge/internal/localoperator"
)

func localOperatorAdmission(ingress *localoperator.Ingress, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Static UI and readiness expose no operator capability. The sibling MCP
		// routers in main.runServer never traverse this local-control handler.
		if !strings.HasPrefix(r.URL.Path, "/ui/api/") && !strings.HasPrefix(r.URL.Path, "/v1/") {
			next.ServeHTTP(w, r)
			return
		}
		ctx, err := ingress.AdmitHTTP(r)
		if err != nil {
			writeJSON(w, http.StatusForbidden, map[string]string{"message": "Trusted local control-plane access required", "code": "LOCAL_OPERATOR_ACCESS_DENIED"})
			return
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
