package mcpserver

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultMCPEndpoint       = "/mcp"
	defaultMaxRequestBytes   = 1 << 20
	defaultReadHeaderTimeout = 5 * time.Second
	defaultIdleTimeout       = 60 * time.Second
)

// HTTPConfig configures the loopback-only stateless Streamable HTTP server.
type HTTPConfig struct {
	ListenAddress       string
	BearerToken         string
	Endpoint            string
	MaxRequestBodyBytes int64
}

// NewStatelessHTTPServer constructs a loopback-only HTTP server. The returned
// server is not started; callers retain shutdown and listener ownership.
func (s *Server) NewStatelessHTTPServer(config HTTPConfig) (*http.Server, error) {
	if err := validateLoopbackListenAddress(config.ListenAddress); err != nil {
		return nil, err
	}
	bearer, err := BearerMiddleware(config.BearerToken)
	if err != nil {
		return nil, err
	}

	endpoint := config.Endpoint
	if endpoint == "" {
		endpoint = defaultMCPEndpoint
	}
	if endpoint[0] != '/' || strings.ContainsAny(endpoint, "?#") {
		return nil, errors.New("mcpserver: HTTP endpoint must be an absolute path without query or fragment")
	}
	maxRequestBytes := config.MaxRequestBodyBytes
	if maxRequestBytes == 0 {
		maxRequestBytes = defaultMaxRequestBytes
	}
	if maxRequestBytes < 0 {
		return nil, errors.New("mcpserver: HTTP request body limit must be positive")
	}

	streamable := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return s.newProtocolServer() },
		&mcp.StreamableHTTPOptions{
			Stateless:                    true,
			JSONResponse:                 true,
			MaxRequestBodyBytes:          maxRequestBytes,
			PropagateRequestCancellation: true,
		},
	)
	originProtection := new(http.CrossOriginProtection)
	mux := http.NewServeMux()
	mux.Handle(endpoint, bearer(originProtection.Handler(streamable)))

	return &http.Server{
		Addr:              config.ListenAddress,
		Handler:           mux,
		ReadHeaderTimeout: defaultReadHeaderTimeout,
		IdleTimeout:       defaultIdleTimeout,
		MaxHeaderBytes:    32 << 10,
	}, nil
}

// BearerMiddleware returns independent HTTP bearer authentication middleware.
// It never forwards the bearer credential to DeviceService.
func BearerMiddleware(token string) (func(http.Handler) http.Handler, error) {
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return nil, errors.New("mcpserver: bearer token must be a non-empty token without whitespace")
	}
	tokenBytes := []byte(token)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			values := request.Header.Values("Authorization")
			if len(values) != 1 {
				unauthorized(response)
				return
			}
			scheme, credential, ok := strings.Cut(values[0], " ")
			if !ok || !strings.EqualFold(scheme, "Bearer") || subtle.ConstantTimeCompare([]byte(credential), tokenBytes) != 1 {
				unauthorized(response)
				return
			}
			next.ServeHTTP(response, request)
		})
	}, nil
}

func unauthorized(response http.ResponseWriter) {
	response.Header().Set("WWW-Authenticate", `Bearer realm="jetkvm-mcp"`)
	http.Error(response, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
}

func validateLoopbackListenAddress(address string) error {
	addrPort, err := netip.ParseAddrPort(address)
	if err != nil {
		return errors.New("mcpserver: HTTP listen address must be an IP literal with port")
	}
	if !addrPort.Addr().IsLoopback() {
		return errors.New("mcpserver: HTTP listen address must be loopback")
	}
	return nil
}
