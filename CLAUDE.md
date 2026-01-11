# CLAUDE.md

## Code Style

- NO COMMENTS in code. Self-documenting through clear naming only.
- NO backward compatibility - delete old code, don't keep dead code
- NO suggestions - follow instructions exactly
- Replace existing code, don't add alongside
- NEVER assume - always read files and clarify gaps before implementing
- ASK questions when requirements are unclear

## SDK Architecture

Follows Temporal SDK pattern: single Worker instance that can both submit workflows AND execute tasks.

### Single Worker Model

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
```

One registration. One worker. Three worker pools:
1. **Submit Pool** - ExecuteTasks batch RPC
2. **Result Pool** - GetTaskResults batch RPC
3. **Poll Pool** - PollTask for receiving tasks to execute

### Worker Lifecycle

```go
worker := NewWorker(serverAddr, taskTypes)  // RegisterWorker called once
worker.RegisterTaskHandler("echo", echoHandler)
worker.Start()  // Starts all three pools

// Can now:
// - Execute workflows (uses Submit + Result pools)
// - Poll and execute tasks (uses Poll pool)
```

### Task Pool (Channel-Based Backpressure)

```go
type TaskPool interface {
    SubmitCh() chan<- []*SubmitRequest      // batch input, blocks when full
    ResultCh() chan<- []*ResultRequest      // batch input, blocks when full

    SubmitRespCh() <-chan []*SubmitResponse // batch output
    ResultRespCh() <-chan []*ResultResponse // batch output

    Errors() <-chan error

    Start()
    Stop()
}
```

- Workers consume from input channels
- Backpressure = channel blocks when buffer full
- Batch unary calls (list of tasks)
- Router streams batches to leader

### Proto RPCs

```protobuf
rpc RegisterWorker(RegisterWorkerRequest) returns (RegisterWorkerResponse);
rpc PollTask(PollTaskRequest) returns (TaskRequest);
rpc CompleteTask(TaskResult) returns (CompleteTaskResponse);
rpc ExecuteTasks(ExecuteTasksRequest) returns (ExecuteTasksResponse);
rpc GetTaskResults(GetTaskResultsRequest) returns (GetTaskResultsResponse);
```

### Pool Config

```go
type PoolConfig struct {
    SubmitWorkers int   // workers consuming from SubmitCh
    ResultWorkers int   // workers consuming from ResultCh
    PollWorkers   int   // workers polling for tasks
    BufferSize    int   // channel buffer (e.g., 100)
}
```