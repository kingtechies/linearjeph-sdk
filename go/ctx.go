package linearjeph

import (
	"context"
	"net/http"
	"time"
)

// withTimeout attaches a per-attempt deadline to a request.
func withTimeout(req *http.Request, d time.Duration) (*http.Request, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(req.Context(), d)
	return req.WithContext(ctx), cancel
}
