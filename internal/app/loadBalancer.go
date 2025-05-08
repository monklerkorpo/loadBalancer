package app

import (
    "flag"
    "fmt"
    "log"
    "os"
    "os/signal"
    "syscall"

    "github.com/Manzo48/loadBalancer/internal/config"
    "github.com/Manzo48/loadBalancer/internal/proxy"
    "go.uber.org/zap"
)

func Run() {
    configPath := flag.String("config", "config.yaml", "path to configuration file")
    flag.Parse()

    logger, err := zap.NewProduction()
    if err != nil {
        log.Fatalf("failed to initialize logger: %v", err)
    }
    defer logger.Sync()
    sugar := logger.Sugar()

    cfg, err := config.Load(*configPath)
    if err != nil {
        sugar.Fatalf("failed to load config: %v", err)
    }

    lb := proxy.NewProxyServer(cfg, sugar)

    go func() {
        addr := fmt.Sprintf(":%d", cfg.Port)
        if err := lb.Start(addr); err != nil {
            sugar.Fatalf("server failed: %v", err)
        }
    }()

    quit := make(chan os.Signal, 1)
    signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
    <-quit

    sugar.Info("received shutdown signal")
    lb.Shutdown()
}
