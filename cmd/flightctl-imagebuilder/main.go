package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	apiclient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/imagebuilder"
	"github.com/flightctl/flightctl/internal/instrumentation"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/sirupsen/logrus"
)

func main() {
	ctx := context.Background()

	log := log.InitLogs()
	log.Println("Starting flightctl-imagebuilder service")
	defer log.Println("flightctl-imagebuilder service stopped")

	cfg, err := config.LoadOrGenerate(config.ConfigFile())
	if err != nil {
		log.Fatalf("reading configuration: %v", err)
	}
	log.Printf("Using config: %s", cfg)

	logLvl, err := logrus.ParseLevel(cfg.Service.LogLevel)
	if err != nil {
		logLvl = logrus.InfoLevel
	}
	log.SetLevel(logLvl)

	tracerShutdown := instrumentation.InitTracer(log, cfg, "flightctl-imagebuilder")
	defer func() {
		if err := tracerShutdown(ctx); err != nil {
			log.Fatalf("failed to shut down tracer: %v", err)
		}
	}()

	// Initialize database store
	log.Println("Initializing data store")
	db, err := store.InitDB(cfg, log)
	if err != nil {
		log.Fatalf("initializing data store: %v", err)
	}

	dataStore := store.NewStore(db, log.WithField("pkg", "store"))
	defer dataStore.Close()

	// Initialize flightctl API client
	log.Println("Initializing flightctl API client")
	apiClient, err := createAPIClient(cfg, log)
	if err != nil {
		log.Fatalf("failed to create API client: %v", err)
	}

	// Initialize Kubernetes client
	log.Println("Initializing Kubernetes client")
	k8sClient, err := k8sclient.NewK8SClient()
	if err != nil {
		log.Fatalf("failed to initialize Kubernetes client: %v", err)
	}

	// Create orchestrator
	orchestrator := imagebuilder.NewOrchestrator(apiClient, k8sClient, cfg, log.WithField("component", "orchestrator"))

	// Create build manager
	buildManager := imagebuilder.NewBuildManager(dataStore, orchestrator, k8sClient, cfg, log)

	// Create cleanup manager
	cleanupManager := imagebuilder.NewCleanupManager(dataStore, k8sClient, cfg, log)

	// Create HTTP server
	httpServer := imagebuilder.NewHTTPServer(k8sClient, cfg, orchestrator, log)

	// Setup signal handling
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGQUIT)
	defer cancel()

	// Start HTTP server
	server := httpServer.Start(ctx)
	
	// Perform initial cleanup of orphaned resources
	if err := cleanupManager.PerformInitialCleanup(ctx); err != nil {
		log.WithError(err).Warn("Initial cleanup failed, continuing anyway")
	}
	
	// Start build manager polling loop
	log.Println("Starting build manager polling loop")
	if err := buildManager.Run(ctx); err != nil {
		log.Fatalf("Error running build manager: %s", err)
	}
	
	// Shutdown HTTP server gracefully
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Errorf("HTTP server shutdown error: %v", err)
	}
}

// createAPIClient creates a flightctl API client for internal service-to-service communication
func createAPIClient(cfg *config.Config, log logrus.FieldLogger) (*apiclient.ClientWithResponses, error) {
	baseURL := cfg.Service.BaseUrl
	if baseURL == "" {
		return nil, fmt.Errorf("service base URL not configured")
	}

	clientConfig := &client.Config{
		Service: client.Service{
			Server:             baseURL,
			InsecureSkipVerify: true, // Skip TLS verification for internal service-to-service communication
		},
	}
	
	apiClient, err := client.NewFromConfig(clientConfig, "")
	if err != nil {
		return nil, fmt.Errorf("failed to create API client: %w", err)
	}

	return apiClient, nil
}
