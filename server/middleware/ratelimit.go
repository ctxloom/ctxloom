// Package middleware provides HTTP and gRPC middleware for the fragment service.
package middleware

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// RateLimitConfig configures the rate limiter.
type RateLimitConfig struct {
	// ReadRPM is requests per minute for read operations (get, list, search)
	ReadRPM int
	// WriteRPM is requests per minute for write operations (create, update, delete)
	WriteRPM int
	// EngagementRPM is requests per minute for engagement operations (like, dislike, download)
	EngagementRPM int
	// BurstMultiplier allows burst traffic (e.g., 2.0 allows 2x burst)
	BurstMultiplier float64
	// WindowDuration is the time window for rate limiting
	WindowDuration time.Duration
	// CleanupInterval is how often to clean up old entries
	CleanupInterval time.Duration
}

// DefaultRateLimitConfig returns sensible defaults for rate limiting.
func DefaultRateLimitConfig() RateLimitConfig {
	return RateLimitConfig{
		ReadRPM:         300,  // 5 requests per second
		WriteRPM:        30,   // 0.5 requests per second
		EngagementRPM:   60,   // 1 request per second
		BurstMultiplier: 2.0,  // Allow 2x burst
		WindowDuration:  time.Minute,
		CleanupInterval: 5 * time.Minute,
	}
}

// RateLimiter implements a sliding window rate limiter.
type RateLimiter struct {
	config   RateLimitConfig
	mu       sync.RWMutex
	clients  map[string]*clientState
	stopChan chan struct{}
}

type clientState struct {
	readRequests       []time.Time
	writeRequests      []time.Time
	engagementRequests []time.Time
}

// OperationType categorizes request types for rate limiting.
type OperationType int

const (
	OpRead OperationType = iota
	OpWrite
	OpEngagement
)

// NewRateLimiter creates a new rate limiter with the given configuration.
func NewRateLimiter(config RateLimitConfig) *RateLimiter {
	rl := &RateLimiter{
		config:   config,
		clients:  make(map[string]*clientState),
		stopChan: make(chan struct{}),
	}

	// Start cleanup goroutine
	go rl.cleanup()

	return rl
}

// Stop stops the rate limiter's cleanup goroutine.
func (rl *RateLimiter) Stop() {
	close(rl.stopChan)
}

func (rl *RateLimiter) cleanup() {
	ticker := time.NewTicker(rl.config.CleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			rl.mu.Lock()
			cutoff := time.Now().Add(-rl.config.WindowDuration)
			for key, state := range rl.clients {
				state.readRequests = filterRecent(state.readRequests, cutoff)
				state.writeRequests = filterRecent(state.writeRequests, cutoff)
				state.engagementRequests = filterRecent(state.engagementRequests, cutoff)

				// Remove empty entries
				if len(state.readRequests) == 0 && len(state.writeRequests) == 0 && len(state.engagementRequests) == 0 {
					delete(rl.clients, key)
				}
			}
			rl.mu.Unlock()
		case <-rl.stopChan:
			return
		}
	}
}

func filterRecent(times []time.Time, cutoff time.Time) []time.Time {
	var result []time.Time
	for _, t := range times {
		if t.After(cutoff) {
			result = append(result, t)
		}
	}
	return result
}

// Allow checks if a request should be allowed based on rate limits.
func (rl *RateLimiter) Allow(clientID string, opType OperationType) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-rl.config.WindowDuration)

	state, exists := rl.clients[clientID]
	if !exists {
		state = &clientState{}
		rl.clients[clientID] = state
	}

	var limit int
	var requests *[]time.Time

	switch opType {
	case OpRead:
		limit = int(float64(rl.config.ReadRPM) * rl.config.BurstMultiplier)
		requests = &state.readRequests
	case OpWrite:
		limit = int(float64(rl.config.WriteRPM) * rl.config.BurstMultiplier)
		requests = &state.writeRequests
	case OpEngagement:
		limit = int(float64(rl.config.EngagementRPM) * rl.config.BurstMultiplier)
		requests = &state.engagementRequests
	}

	// Filter out old requests
	*requests = filterRecent(*requests, cutoff)

	// Check if under limit
	if len(*requests) >= limit {
		return false
	}

	// Record the request
	*requests = append(*requests, now)
	return true
}

// GRPCUnaryInterceptor returns a gRPC unary interceptor for rate limiting.
func (rl *RateLimiter) GRPCUnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		clientID := extractClientID(ctx)
		opType := classifyGRPCMethod(info.FullMethod)

		if !rl.Allow(clientID, opType) {
			return nil, status.Errorf(codes.ResourceExhausted, "rate limit exceeded")
		}

		return handler(ctx, req)
	}
}

// HTTPMiddleware returns an HTTP middleware for rate limiting.
func (rl *RateLimiter) HTTPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clientID := extractClientIDFromHTTP(r)
		opType := classifyHTTPMethod(r.Method, r.URL.Path)

		if !rl.Allow(clientID, opType) {
			http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func extractClientID(ctx context.Context) string {
	// Try to get from metadata (e.g., API key, user ID)
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if values := md.Get("x-api-key"); len(values) > 0 {
			return "apikey:" + values[0]
		}
		if values := md.Get("x-user-id"); len(values) > 0 {
			return "user:" + values[0]
		}
		// Check forwarded headers
		if values := md.Get("x-forwarded-for"); len(values) > 0 {
			return "ip:" + strings.Split(values[0], ",")[0]
		}
	}

	// Fall back to peer address
	if p, ok := peer.FromContext(ctx); ok {
		if addr, ok := p.Addr.(*net.TCPAddr); ok {
			return "ip:" + addr.IP.String()
		}
		return "ip:" + p.Addr.String()
	}

	return "unknown"
}

func extractClientIDFromHTTP(r *http.Request) string {
	// Check for API key
	if apiKey := r.Header.Get("X-API-Key"); apiKey != "" {
		return "apikey:" + apiKey
	}

	// Check for user ID
	if userID := r.Header.Get("X-User-ID"); userID != "" {
		return "user:" + userID
	}

	// Check X-Forwarded-For
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return "ip:" + strings.Split(xff, ",")[0]
	}

	// Check X-Real-IP
	if realIP := r.Header.Get("X-Real-IP"); realIP != "" {
		return "ip:" + realIP
	}

	// Fall back to remote address
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return "ip:" + host
}

func classifyGRPCMethod(method string) OperationType {
	switch {
	case strings.Contains(method, "Create") || strings.Contains(method, "Update") || strings.Contains(method, "Delete"):
		return OpWrite
	case strings.Contains(method, "Like") || strings.Contains(method, "Dislike") || strings.Contains(method, "Download"):
		return OpEngagement
	default:
		return OpRead
	}
}

func classifyHTTPMethod(method, path string) OperationType {
	switch method {
	case "POST":
		if strings.Contains(path, "/like") || strings.Contains(path, "/dislike") || strings.Contains(path, "/download") {
			return OpEngagement
		}
		return OpWrite
	case "PUT", "DELETE", "PATCH":
		return OpWrite
	default:
		return OpRead
	}
}
