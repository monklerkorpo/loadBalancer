package proxy

import (
    "context"
    "encoding/json"
    "net"
    "net/http"
    "net/http/httputil"
    "strings"
    "time"

    "github.com/Manzo48/loadBalancer/internal/balancer"
    "github.com/Manzo48/loadBalancer/internal/config"
    "github.com/Manzo48/loadBalancer/internal/ratelimiter"
    "go.uber.org/zap"
)

// errorResponse определяет формат JSON-ошибок для клиента.
type errorResponse struct {
    Code    int    `json:"code"`
    Message string `json:"message"`
}

// sendJSONError отвечает клиенту с JSON-ошибкой.
func sendJSONError(w http.ResponseWriter, statusCode int, message string) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(statusCode)
    json.NewEncoder(w).Encode(errorResponse{Code: statusCode, Message: message})
}

// ProxyServer реализует прокси с поддержкой балансировки нагрузки и ограничения частоты.
type ProxyServer struct {
    balancer     balancer.LoadBalancer       // Интерфейс балансировщика (например, RoundRobin)
    logger       *zap.SugaredLogger
    httpServer   *http.Server
    rateLimiter  *ratelimiter.RateLimiter
}

// NewProxyServer инициализирует новый экземпляр ProxyServer.
func NewProxyServer(cfg *config.Config, logger *zap.SugaredLogger) *ProxyServer {
    loadBalancer := balancer.NewRoundRobinLoadBalancer(cfg.Backends, logger)
    limiter := ratelimiter.NewRateLimiter(cfg.RateLimit.Capacity, cfg.RateLimit.RefillRate, logger)

    proxy := &ProxyServer{
        balancer:    loadBalancer,
        logger:      logger,
        rateLimiter: limiter,
    }

    logger.Infof("ProxyServer initialized on port %d with %d backends and rate limit %d/%ds",
        cfg.Port, len(cfg.Backends), cfg.RateLimit.Capacity, cfg.RateLimit.RefillRate)

    go proxy.cleanupStaleClients()

    return proxy
}

// Start запускает HTTP-прокси-сервер.
func (p *ProxyServer) Start(addr string) error {
    mux := http.NewServeMux()
    mux.HandleFunc("/", p.handleProxy)

    handlerWithRateLimit := ratelimiter.RateLimitMiddleware(p.rateLimiter, p.logger)(mux)

    p.httpServer = &http.Server{
        Addr:    addr,
        Handler: handlerWithRateLimit,
    }

    p.logger.Infof("Starting proxy server at %s", addr)
    return p.httpServer.ListenAndServe()
}

// Shutdown корректно завершает работу сервера.
func (p *ProxyServer) Shutdown() {
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()

    p.logger.Info("Shutting down proxy server...")
    if err := p.httpServer.Shutdown(ctx); err != nil {
        p.logger.Errorf("Graceful shutdown failed: %v", err)
    } else {
        p.logger.Info("Shutdown complete")
    }
}

// handleProxy обрабатывает входящие HTTP-запросы и выполняет проксирование.
func (p *ProxyServer) handleProxy(w http.ResponseWriter, r *http.Request) {
    clientIP := getClientIP(r)

    target := p.balancer.NextAvailableBackend()
    if target == nil {
        p.logger.Warn("No available backends")
        sendJSONError(w, http.StatusServiceUnavailable, "No available backends")
        return
    }

    proxy := httputil.NewSingleHostReverseProxy(target.Address)

    originalDirector := proxy.Director
    proxy.Director = func(req *http.Request) {
        originalDirector(req)
        req.Host = target.Address.Host
    }

    proxy.ErrorHandler = func(rw http.ResponseWriter, req *http.Request, err error) {
        p.logger.Errorf("Proxy error for backend %s: %v", target.Address, err)
        p.balancer.MarkBackendUnhealthy(target.Address)
        sendJSONError(rw, http.StatusServiceUnavailable, "Backend unavailable")
    }

    p.logger.Infof("Forwarding request from %s to %s", clientIP, target.Address)
    proxy.ServeHTTP(w, r)
}

// cleanupStaleClients запускает периодическую очистку старых записей rate limiter-а.
func (p *ProxyServer) cleanupStaleClients() {
    ticker := time.NewTicker(1 * time.Minute)
    defer ticker.Stop()

    for range ticker.C {
        p.logger.Debug("Running rate limiter cleanup")
        p.rateLimiter.Cleanup(5 * time.Minute)
    }
}

// getClientIP извлекает IP-адрес клиента из заголовков или соединения.
func getClientIP(r *http.Request) string {
    if ip := r.Header.Get("X-Real-IP"); ip != "" {
        return strings.TrimSpace(ip)
    }
    if ip := r.Header.Get("X-Forwarded-For"); ip != "" {
        return strings.TrimSpace(strings.Split(ip, ",")[0])
    }
    ip, _, _ := net.SplitHostPort(r.RemoteAddr)
    return strings.TrimSpace(ip)
}
