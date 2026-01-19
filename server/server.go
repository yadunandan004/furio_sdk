package server

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"furio_sdk/workflow"
)

type Server struct {
	worker     *workflow.Worker
	workerID   string
	httpAddr   string
	httpServer *http.Server
	startTime  time.Time
	mu         sync.RWMutex
}

type Config struct {
	WorkerID      string
	ServerAddr    string
	HTTPAddr      string
	PollTimeoutMs int32
	PoolConfig    workflow.PoolConfig
}

func New(cfg Config) *Server {
	workerCfg := workflow.Config{
		ServerAddr:    cfg.ServerAddr,
		PollTimeoutMs: cfg.PollTimeoutMs,
		PoolConfig:    cfg.PoolConfig,
	}

	return &Server{
		worker:   workflow.NewWorker(workerCfg),
		workerID: cfg.WorkerID,
		httpAddr: cfg.HTTPAddr,
	}
}

func (s *Server) RegisterHandler(name string, handler workflow.TaskHandler, description string) {
	s.worker.RegisterTaskWithOptions(name, handler, description)
}

func (s *Server) Start() error {
	s.startTime = time.Now()

	if err := s.worker.Connect(); err != nil {
		return fmt.Errorf("failed to connect worker: %w", err)
	}

	if err := s.worker.Start(); err != nil {
		return fmt.Errorf("failed to start worker: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /workflow", s.handleSubmitWorkflow)
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /metrics", s.handleMetrics)

	s.httpServer = &http.Server{
		Addr:         s.httpAddr,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Printf("[Server] HTTP server starting on %s", s.httpAddr)
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("[Server] HTTP server error: %v", err)
		}
	}()

	log.Printf("[Server] Started - Worker ID: %s, Node ID: %d, HTTP: %s",
		s.workerID, s.worker.NodeID(), s.httpAddr)

	return nil
}

func (s *Server) Stop() error {
	log.Printf("[Server] Stopping...")

	if s.httpServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.httpServer.Shutdown(ctx); err != nil {
			log.Printf("[Server] HTTP shutdown error: %v", err)
		}
	}

	if err := s.worker.Stop(); err != nil {
		return fmt.Errorf("failed to stop worker: %w", err)
	}

	log.Printf("[Server] Stopped")
	return nil
}

func (s *Server) Worker() *workflow.Worker {
	return s.worker
}

func (s *Server) WorkerID() string {
	return s.workerID
}

func (s *Server) NodeID() int64 {
	return s.worker.NodeID()
}

func (s *Server) Uptime() time.Duration {
	return time.Since(s.startTime)
}
