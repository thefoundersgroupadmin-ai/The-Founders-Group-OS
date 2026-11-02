package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// noBadGateway turns a 502 from a browser route into 424 Failed Dependency.
// The OS is served through a Cloudflare tunnel, which replaces an origin 502
// with its own HTML error page: the owner then saw that HTML instead of the
// route's JSON reason (Approvals, 2 Nov 2026). 424 says the same thing (a
// service this one depends on failed) and Cloudflare passes it through.
func noBadGateway(c *gin.Context) {
	c.Writer = &noBadGatewayWriter{ResponseWriter: c.Writer}
	c.Next()
}

type noBadGatewayWriter struct{ gin.ResponseWriter }

func (w *noBadGatewayWriter) WriteHeader(code int) {
	if code == http.StatusBadGateway {
		code = http.StatusFailedDependency
	}
	w.ResponseWriter.WriteHeader(code)
}
