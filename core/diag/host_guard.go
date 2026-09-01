package diag

import (
	"net"
	"net/http"
	"strings"
)

// loopbackHostGuard rejects requests whose HTTP Host does not name localhost or a
// loopback IP. Binding the listener to loopback is not enough on its own: a browser
// can be tricked by DNS rebinding into treating an attacker-controlled hostname as
// same-origin after that hostname resolves to 127.0.0.1. In that case CORS provides
// no protection because the browser believes the request is same-origin.
func loopbackHostGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isLoopbackRequestHost(r.Host) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isLoopbackRequestHost(hostport string) bool {
	hostport = strings.TrimSpace(hostport)
	if hostport == "" {
		return false
	}

	host := hostport
	if parsedHost, _, err := net.SplitHostPort(hostport); err == nil {
		host = parsedHost
	} else {
		// A Host header containing ':' must use the bracketed IPv6 or host:port
		// syntax accepted by SplitHostPort. Reject ambiguous/bare forms rather than
		// guessing at a security boundary.
		if strings.Contains(hostport, ":") {
			return false
		}
	}

	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
