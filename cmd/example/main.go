package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	pb "furio_sdk/proto"
	"furio_sdk/workflow"
)

func main() {
	serverAddr := flag.String("server", "localhost:9090", "Furio server address")
	pollTimeout := flag.Int("poll-timeout", 30000, "Poll timeout in milliseconds")
	flag.Parse()

	log.Printf("Starting Furio SDK Worker")
	log.Printf("  Server: %s", *serverAddr)
	log.Printf("  Poll Timeout: %dms", *pollTimeout)

	w := workflow.NewWorker(workflow.Config{
		ServerAddr:    *serverAddr,
		PollTimeoutMs: int32(*pollTimeout),
	})

	w.OnFailure(func(notification *pb.WorkflowFailureNotification) {
		log.Printf("[FAILURE CALLBACK] Workflow %d Task %d (%s) failed: %s (type=%s)",
			notification.WorkflowId, notification.TaskId, notification.TaskType,
			notification.Error, notification.FailureType.String())
	})

	w.RegisterTaskWithOptions("dummy", workflow.DummyHandler, "Dummy handler for load testing")
	w.RegisterTaskWithOptions("noop", workflow.NoOpHandler, "No-op handler - fastest possible")
	w.RegisterTaskWithOptions("echo", workflow.EchoHandler, "Echo handler - returns input as output")
	w.RegisterTaskWithOptions("delay", workflow.DelayHandler, "Delay handler - simulates IO-bound work")
	w.RegisterTaskWithOptions("cpu_work", workflow.CPUWorkHandler, "CPU work handler - simulates compute-bound work")
	w.RegisterTaskWithOptions("fail", workflow.FailHandler, "Fail handler - always fails for testing failure callback")

	if err := w.Connect(); err != nil {
		log.Fatalf("Failed to connect to server: %v", err)
	}

	if err := w.Start(); err != nil {
		log.Fatalf("Failed to start worker: %v", err)
	}

	log.Printf("Worker is running. Press Ctrl+C to stop.")
	log.Printf("You can now run workflows using this worker.")

	go runSampleWorkflow(w)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Printf("Shutdown signal received")

	if err := w.Stop(); err != nil {
		log.Printf("Error during shutdown: %v", err)
	}

	log.Printf("Worker stopped")
}

func runSampleWorkflow(w *workflow.Worker) {
	time.Sleep(2 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	log.Printf("Executing sample workflow...")
	start := time.Now()

	err := w.ExecuteWorkflow(ctx, sampleWorkflow,
		workflow.WithFailureCallback(true),
		workflow.WithWorkflowName("sample-workflow"),
	)
	if err != nil {
		log.Printf("Sample workflow failed: %v", err)
		return
	}

	log.Printf("Sample workflow completed successfully in %v", time.Since(start))
}

func sampleWorkflow(ctx *workflow.Context) error {
	log.Printf("[Workflow %d] Starting...", ctx.WorkflowID())

	log.Printf("[Workflow %d] Executing echo task...", ctx.WorkflowID())
	result1, err := ctx.ExecuteTask("echo", map[string]string{"message": "hello from workflow"})
	if err != nil {
		return err
	}
	log.Printf("[Workflow %d] Echo result: %s", ctx.WorkflowID(), string(result1))

	log.Printf("[Workflow %d] Executing CPU work task...", ctx.WorkflowID())
	result2, err := ctx.ExecuteTask("cpu_work", nil)
	if err != nil {
		return err
	}

	var cpuResult map[string]interface{}
	json.Unmarshal(result2, &cpuResult)
	log.Printf("[Workflow %d] CPU work result: %v", ctx.WorkflowID(), cpuResult)

	log.Printf("[Workflow %d] All tasks completed!", ctx.WorkflowID())
	return nil
}
