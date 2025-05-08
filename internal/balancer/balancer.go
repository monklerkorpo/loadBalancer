package balancer

import (
    "net/http"
    "net/url"
    "sync/atomic"
    "time"

    "go.uber.org/zap"
)

// Backend представляет один сервер, обрабатывающий клиентские запросы.
type Backend struct {
    Address *url.URL    // Адрес backend-сервера
    IsAlive atomic.Bool // Флаг доступности (жив ли сервер)
}

// LoadBalancer описывает поведение балансировщика.
type LoadBalancer interface {
    NextAvailableBackend() *Backend
    MarkBackendUnhealthy(target *url.URL)
}

// RoundRobinLoadBalancer реализует интерфейс LoadBalancer по алгоритму Round-Robin.
type RoundRobinLoadBalancer struct {
    backends       []*Backend         // Список всех backend-серверов
    currentIndex   uint32             // Текущий индекс для round-robin
    logger         *zap.SugaredLogger // Логгер

    healthCheckInterval time.Duration // Интервал между health-check запросами
    healthCheckTimeout  time.Duration // Таймаут запроса health-check
}

// NewRoundRobinLoadBalancer создает новый RoundRobinLoadBalancer и запускает цикл health-check.
func NewRoundRobinLoadBalancer(backendURLs []string, logger *zap.SugaredLogger) *RoundRobinLoadBalancer {
    loadBalancer := &RoundRobinLoadBalancer{
        backends:             make([]*Backend, 0, len(backendURLs)),
        logger:               logger,
        healthCheckInterval:  10 * time.Second,
        healthCheckTimeout:   2 * time.Second,
    }

    for _, rawURL := range backendURLs {
        parsedURL, err := url.Parse(rawURL)
        if err != nil {
            logger.Warnf("Invalid backend URL %s: %v", rawURL, err)
            continue
        }

        backend := &Backend{Address: parsedURL}
        backend.IsAlive.Store(true) // Считаем, что backend жив на старте
        loadBalancer.backends = append(loadBalancer.backends, backend)

        logger.Infof("Backend registered: %s", parsedURL.String())
    }

    go loadBalancer.runHealthCheckLoop()

    return loadBalancer
}

// runHealthCheckLoop периодически проверяет доступность всех backend'ов.
func (lb *RoundRobinLoadBalancer) runHealthCheckLoop() {
    client := &http.Client{Timeout: lb.healthCheckTimeout}
    ticker := time.NewTicker(lb.healthCheckInterval)
    defer ticker.Stop()

    for range ticker.C {
        for _, backend := range lb.backends {
            go func(b *Backend) {
                healthCheckURL := b.Address.String() + "/health"
                response, err := client.Get(healthCheckURL)

                isHealthy := err == nil && response.StatusCode == http.StatusOK
                b.IsAlive.Store(isHealthy)

                if isHealthy {
                    lb.logger.Debugf("Health check passed: %s", b.Address)
                } else {
                    lb.logger.Warnf("Health check failed: %s (error: %v)", b.Address, err)
                }

                if response != nil {
                    response.Body.Close()
                }
            }(backend)
        }
    }
}

// NextAvailableBackend возвращает следующий доступный backend по алгоритму Round-Robin.
func (lb *RoundRobinLoadBalancer) NextAvailableBackend() *Backend {
    total := len(lb.backends)
    for attempt := 0; attempt < total; attempt++ {
        index := atomic.AddUint32(&lb.currentIndex, 1) % uint32(total)
        candidate := lb.backends[index]

        if candidate.IsAlive.Load() {
            lb.logger.Debugf("Backend selected: %s", candidate.Address)
            return candidate
        }
    }

    lb.logger.Warn("No healthy backends available")
    return nil
}

// MarkBackendUnhealthy помечает указанный backend как недоступный.
func (lb *RoundRobinLoadBalancer) MarkBackendUnhealthy(target *url.URL) {
    for _, backend := range lb.backends {
        if backend.Address.String() == target.String() {
            backend.IsAlive.Store(false)
            lb.logger.Warnf("Backend marked as unhealthy: %s", target)
            return
        }
    }
}
