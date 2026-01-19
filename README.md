# Furio SDK

Go client SDK for [Furio](https://github.com/yourorg/furio) - a distributed workflow orchestration library.

## Overview

The Furio SDK enables applications to:
- **Execute workflows** - Submit multi-step workflows to the Furio cluster
- **Run workers** - Register task handlers and process tasks
- **Handle failures** - Subscribe to workflow failure notifications

Follows the Temporal SDK pattern: a single Worker instance that can both submit workflows AND execute tasks.

## Installation

```bash
go get github.com/yourorg/furio-sdk
```

## Quick Start

```go
package main

import (
    "context"
    "log"

    "github.com/yourorg/furio-sdk/workflow"
)

func main() {
    // Create worker
    w := workflow.NewWorker(workflow.Config{
        ServerAddr:    "localhost:9090",
        PollTimeoutMs: 30000,
    })

    // Register task handlers
    w.RegisterTask("echo", func(ctx context.Context, input []byte) ([]byte, error) {
        return input, nil // Echo back the input
    })

    // Connect to Furio cluster
    if err := w.Connect(); err != nil {
        log.Fatal(err)
    }
    defer w.Stop()

    // Start polling for tasks
    w.Start()

    // Execute a workflow
    err := w.ExecuteWorkflow(context.Background(), func(ctx *workflow.Context) error {
        result, err := ctx.ExecuteTask("echo", map[string]string{"message": "hello"})
        if err != nil {
            return err
        }
        log.Printf("Result: %s", result)
        return nil
    })
    if err != nil {
        log.Fatal(err)
    }
}
```

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                     SDK Worker Instance                      │
├─────────────────────────────────────────────────────────────┤
│  RegisterWorker(taskTypes) → node_id                        │
│                                                              │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐          │
│  │ Submit Pool │  │ Result Pool │  │ Poll Pool   │          │
│  │ ExecuteTasks│  │GetTaskResults│  │ PollTask    │          │
│  └─────────────┘  └─────────────┘  └─────────────┘          │
│                                                              │
│  Can: submit workflows, wait for results, execute tasks     │
└─────────────────────────────────────────────────────────────┘
         │
         │ gRPC
         ▼
┌─────────────────────────────────────────────────────────────┐
│                      Furio Cluster                           │
│  ┌────────┐    ┌────────┐    ┌────────┐                     │
│  │ Router │◄──►│ Leader │◄──►│ Sentinel│                    │
│  └────────┘    └────────┘    └────────┘                     │
└─────────────────────────────────────────────────────────────┘
```

## Worker Configuration

```go
w := workflow.NewWorker(workflow.Config{
    ServerAddr:    "localhost:9090",  // Furio router address
    PollTimeoutMs: 30000,             // Long-poll timeout (default: 30s)
    PoolConfig: workflow.PoolConfig{
        SubmitWorkers: 5,   // Concurrent workflow submitters
        ResultWorkers: 5,   // Concurrent result fetchers
        PollWorkers:   10,  // Concurrent task pollers
        BufferSize:    100, // Channel buffer size
    },
})
```

## Task Handlers

Register handlers for task types your worker can execute:

```go
// Simple handler
w.RegisterTask("process", func(ctx context.Context, input []byte) ([]byte, error) {
    var data MyInput
    json.Unmarshal(input, &data)

    result := process(data)

    return json.Marshal(result)
})

// Handler with description (for task registry)
w.RegisterTaskWithOptions("validate", validateHandler, "Validates user input")
```

### Built-in Test Handlers

The SDK includes handlers for testing:

| Handler | Description |
|---------|-------------|
| `echo` | Returns input as output |
| `noop` | No-op, fastest possible |
| `dummy` | Load testing handler |
| `delay` | Simulates IO-bound work |
| `cpu_work` | Simulates compute-bound work |
| `fail` | Always fails (for testing failure callbacks) |

## Executing Workflows

### Basic Workflow

```go
err := w.ExecuteWorkflow(ctx, func(ctx *workflow.Context) error {
    // Stage 1: Validate
    _, err := ctx.ExecuteTask("validate", input)
    if err != nil {
        return err
    }

    // Stage 2: Process
    result, err := ctx.ExecuteTask("process", input)
    if err != nil {
        return err
    }

    // Stage 3: Notify
    _, err = ctx.ExecuteTask("notify", result)
    return err
})
```

### Workflow Options

```go
err := w.ExecuteWorkflow(ctx, myWorkflow,
    workflow.WithWorkflowName("order-processing"),
    workflow.WithWorkflowTimeout(60000),        // 60s timeout
    workflow.WithFailureCallback(true),         // Enable failure notifications
)
```

### Task Timeout

```go
// Default 60s timeout
result, err := ctx.ExecuteTask("slow_task", input)

// Custom timeout
result, err := ctx.ExecuteTaskWithTimeout("slow_task", input, 120000) // 120s
```

## Failure Handling

Subscribe to failure notifications for workflows you submit:

```go
w.OnFailure(func(notification *pb.WorkflowFailureNotification) {
    log.Printf("Workflow %d failed: task=%s type=%s error=%s",
        notification.WorkflowId,
        notification.TaskType,
        notification.FailureType,
        notification.Error,
    )
    // Handle failure: retry, alert, compensate, etc.
})
```

## Worker Lifecycle

```go
// 1. Create worker
w := workflow.NewWorker(config)

// 2. Register tasks (before Connect)
w.RegisterTask("task1", handler1)
w.RegisterTask("task2", handler2)

// 3. Connect to cluster (registers tasks and worker)
err := w.Connect()

// 4. Start polling for tasks
err = w.Start()

// 5. Execute workflows as needed
w.ExecuteWorkflow(ctx, myWorkflow)

// 6. Graceful shutdown
w.Stop()
```

## Running the Example

```bash
# Start a worker that polls for tasks and runs a sample workflow
go run cmd/example/main.go -server=localhost:9090

# Flags:
#   -server       Furio server address (default: localhost:9090)
#   -poll-timeout Poll timeout in ms (default: 30000)
```

## gRPC Protocol

The SDK communicates with Furio via gRPC:

| RPC | Description |
|-----|-------------|
| `RegisterWorker` | Register worker with supported task types |
| `UnregisterWorker` | Graceful worker shutdown |
| `PollTask` | Long-poll for tasks to execute |
| `CompleteTask` | Report task completion/failure |
| `RegisterTaskType` | Register task schema with cluster |
| `StartLiveWorkflow` | Begin a new workflow |
| `ExecuteTasks` | Submit tasks to workflow |
| `GetTaskResults` | Fetch task results |
| `CompleteWorkflow` | Signal workflow completion |
| `SubscribeFailures` | Stream failure notifications |

## Connection Model

```
SDK Worker ──► Router ──► Leader ──► PostgreSQL
                 │
                 ├──► Sentinel (failover)
                 └──► Sentinel (failover)
```

- Workers connect to **Routers**, not Leaders directly
- Routers handle task routing based on registered task types
- Leaders own partitions and coordinate workflow execution
- Sentinels provide failover capability

## Requirements

- Go 1.23+
- Running Furio cluster (see [furio](https://github.com/yourorg/furio))