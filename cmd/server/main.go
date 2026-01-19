package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"furio_sdk/server"
	"furio_sdk/workflow"
)

func main() {
	workerID := getEnv("WORKER_ID", "worker-1")
	furioServer := getEnv("FURIO_SERVER", "localhost:9090")
	httpPort := getEnv("HTTP_PORT", "8080")

	log.Printf("Starting Furio SDK Server")
	log.Printf("  Worker ID: %s", workerID)
	log.Printf("  Furio Server: %s", furioServer)
	log.Printf("  HTTP Port: %s", httpPort)

	srv := server.New(server.Config{
		WorkerID:      workerID,
		ServerAddr:    furioServer,
		HTTPAddr:      ":" + httpPort,
		PollTimeoutMs: 30000,
		PoolConfig: workflow.PoolConfig{
			PollWorkers: 10,
		},
	})

	srv.RegisterHandler("dummy", workflow.DummyHandler, "Dummy handler for load testing")
	srv.RegisterHandler("noop", workflow.NoOpHandler, "No-op handler - fastest possible")
	srv.RegisterHandler("echo", workflow.EchoHandler, "Echo handler - returns input as output")
	srv.RegisterHandler("delay", workflow.DelayHandler, "Delay handler - simulates IO-bound work")
	srv.RegisterHandler("cpu_work", workflow.CPUWorkHandler, "CPU work handler - simulates compute-bound work")
	srv.RegisterHandler("fail", workflow.FailHandler, "Fail handler - always fails for testing")

	if err := srv.Start(); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}

	log.Printf("Server is running. Press Ctrl+C to stop.")
	log.Printf("  Health: http://localhost:%s/health", httpPort)
	log.Printf("  Metrics: http://localhost:%s/metrics", httpPort)
	log.Printf("  Submit workflow: POST http://localhost:%s/workflow", httpPort)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Printf("Shutdown signal received")

	if err := srv.Stop(); err != nil {
		log.Printf("Error during shutdown: %v", err)
	}

	log.Printf("Server stopped")
}

func getEnv(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}
