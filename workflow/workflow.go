package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	pb "furio_sdk/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"
)

type TaskHandler func(ctx context.Context, input []byte) ([]byte, error)

type FailureCallback func(notification *pb.WorkflowFailureNotification)

type TaskDefinition struct {
	Name           string
	Description    string
	Handler        TaskHandler
	DefaultTimeout time.Duration
}

type WorkflowFunc func(ctx *Context) error

type Context struct {
	pool       *TaskPool
	workflowID int64
	nodeID     int64
	ctx        context.Context
	stageIndex int32
	mu         sync.Mutex
}

func (c *Context) ExecuteTask(taskType string, input interface{}) ([]byte, error) {
	return c.ExecuteTaskWithTimeout(taskType, input, 60000)
}

func (c *Context) ExecuteTaskWithTimeout(taskType string, input interface{}, timeoutMs int32) ([]byte, error) {
	inputBytes, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal input: %w", err)
	}

	c.mu.Lock()
	currentStage := c.stageIndex
	c.stageIndex++
	c.mu.Unlock()

	c.pool.SubmitCh() <- []*SubmitRequest{{
		WorkflowID: c.workflowID,
		NodeID:     c.nodeID,
		TaskType:   taskType,
		Input:      inputBytes,
		TimeoutMs:  timeoutMs,
		StageIndex: currentStage,
	}}

	submitResp := <-c.pool.SubmitRespCh()
	if len(submitResp) == 0 || !submitResp[0].Success {
		errMsg := "unknown error"
		if len(submitResp) > 0 {
			errMsg = submitResp[0].Error
		}
		return nil, fmt.Errorf("task submission failed: %s", errMsg)
	}

	taskID := submitResp[0].TaskID

	c.pool.ResultCh() <- []*ResultRequest{{
		WorkflowID: c.workflowID,
		TaskID:     taskID,
		TimeoutMs:  timeoutMs,
	}}

	resultResp := <-c.pool.ResultRespCh()
	if len(resultResp) == 0 {
		return nil, fmt.Errorf("no result returned")
	}
	if resultResp[0].Error != "" {
		return nil, fmt.Errorf("failed to get task result: %s", resultResp[0].Error)
	}
	if !resultResp[0].Ready {
		return nil, fmt.Errorf("task result not ready (timeout)")
	}
	if !resultResp[0].Success {
		return nil, fmt.Errorf("task failed: %s", resultResp[0].Error)
	}

	return resultResp[0].Output, nil
}

func (c *Context) WorkflowID() int64 {
	return c.workflowID
}

func (c *Context) GoContext() context.Context {
	return c.ctx
}

type WorkflowOptions struct {
	FailureCallback bool
	WorkflowName    string
	TimeoutMs       int32
}

type WorkflowOption func(*WorkflowOptions)

func WithFailureCallback(enabled bool) WorkflowOption {
	return func(o *WorkflowOptions) {
		o.FailureCallback = enabled
	}
}

func WithWorkflowName(name string) WorkflowOption {
	return func(o *WorkflowOptions) {
		o.WorkflowName = name
	}
}

func WithWorkflowTimeout(timeoutMs int32) WorkflowOption {
	return func(o *WorkflowOptions) {
		o.TimeoutMs = timeoutMs
	}
}

type Worker struct {
	serverAddr    string
	pollTimeoutMs int32
	poolConfig    PoolConfig

	tasks   map[string]*TaskDefinition
	tasksMu sync.RWMutex

	conn               *grpc.ClientConn
	client             pb.WorkerServiceClient
	taskRegistryClient pb.TaskRegistryServiceClient
	pool               *TaskPool
	nodeID             int64

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	tasksProcessed int64
	tasksFailed    int64
	metricsMu      sync.RWMutex

	failureCallback FailureCallback
	failureMu       sync.RWMutex
}

type Config struct {
	ServerAddr    string
	PollTimeoutMs int32
	PoolConfig    PoolConfig
}

func NewWorker(cfg Config) *Worker {
	if cfg.PollTimeoutMs == 0 {
		cfg.PollTimeoutMs = 30000
	}
	if cfg.PoolConfig.PollWorkers == 0 {
		cfg.PoolConfig.PollWorkers = 10
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &Worker{
		serverAddr:    cfg.ServerAddr,
		pollTimeoutMs: cfg.PollTimeoutMs,
		tasks:         make(map[string]*TaskDefinition),
		ctx:           ctx,
		cancel:        cancel,
		poolConfig:    cfg.PoolConfig,
	}
}

func (w *Worker) RegisterTask(name string, handler TaskHandler) *Worker {
	return w.RegisterTaskWithOptions(name, handler, "")
}

func (w *Worker) RegisterTaskWithOptions(name string, handler TaskHandler, description string) *Worker {
	w.tasksMu.Lock()
	defer w.tasksMu.Unlock()

	w.tasks[name] = &TaskDefinition{
		Name:           name,
		Description:    description,
		Handler:        handler,
		DefaultTimeout: 30 * time.Second,
	}

	log.Printf("[Worker] Registered task type: %s", name)
	return w
}

func (w *Worker) OnFailure(callback FailureCallback) *Worker {
	w.failureMu.Lock()
	defer w.failureMu.Unlock()
	w.failureCallback = callback
	return w
}

func (w *Worker) Connect() error {
	log.Printf("[Worker] Connecting to server at %s", w.serverAddr)

	conn, err := grpc.NewClient(
		w.serverAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                10 * time.Second,
			Timeout:             5 * time.Second,
			PermitWithoutStream: true,
		}),
	)
	if err != nil {
		return fmt.Errorf("failed to connect to server: %w", err)
	}

	w.conn = conn
	w.client = pb.NewWorkerServiceClient(conn)
	w.taskRegistryClient = pb.NewTaskRegistryServiceClient(conn)

	log.Printf("[Worker] Connected to server")

	if err := w.registerTaskTypes(); err != nil {
		return fmt.Errorf("failed to register task types: %w", err)
	}

	if err := w.registerWorker(); err != nil {
		return fmt.Errorf("failed to register worker: %w", err)
	}

	w.pool = NewTaskPool(w.client, w.poolConfig)
	w.pool.Start()

	return nil
}

func (w *Worker) registerTaskTypes() error {
	w.tasksMu.RLock()
	defer w.tasksMu.RUnlock()

	for name, def := range w.tasks {
		ctx, cancel := context.WithTimeout(w.ctx, 10*time.Second)
		resp, err := w.taskRegistryClient.RegisterTaskType(ctx, &pb.RegisterTaskTypeRequest{
			TaskType: &pb.TaskTypeSchema{
				Name:             name,
				Description:      def.Description,
				DefaultTimeoutMs: int32(def.DefaultTimeout.Milliseconds()),
			},
		})
		cancel()

		if err != nil {
			return fmt.Errorf("failed to register task type %s: %w", name, err)
		}

		if !resp.Success {
			return fmt.Errorf("failed to register task type %s: %s", name, resp.Message)
		}

		log.Printf("[Worker] Registered task type with server: %s (id=%d)", name, resp.TaskTypeId)
	}

	return nil
}

func (w *Worker) registerWorker() error {
	w.tasksMu.RLock()
	taskTypes := make([]string, 0, len(w.tasks))
	for name := range w.tasks {
		taskTypes = append(taskTypes, name)
	}
	w.tasksMu.RUnlock()

	ctx, cancel := context.WithTimeout(w.ctx, 10*time.Second)
	defer cancel()

	resp, err := w.client.RegisterWorker(ctx, &pb.RegisterWorkerRequest{
		TaskTypes:          taskTypes,
		MaxConcurrentTasks: int32(w.poolConfig.PollWorkers),
		Metadata:           map[string]string{},
	})
	if err != nil {
		return fmt.Errorf("failed to register worker: %w", err)
	}

	if !resp.Success {
		return fmt.Errorf("worker registration failed: %s", resp.Message)
	}

	w.nodeID = resp.NodeId
	log.Printf("[Worker] Registered with server: node_id=%d task_types=%v", w.nodeID, taskTypes)

	return nil
}

func (w *Worker) Start() error {
	taskTypes := w.getTaskTypeNames()
	if len(taskTypes) == 0 {
		log.Printf("[Worker] No task types registered, skipping poll loops")
		return nil
	}

	log.Printf("[Worker] Starting poll loops for task types: %v", taskTypes)

	for i := 0; i < w.poolConfig.PollWorkers; i++ {
		w.wg.Add(1)
		go w.pollLoop(i)
	}

	w.startFailureSubscription()

	log.Printf("[Worker] Ready to receive tasks")
	return nil
}

func (w *Worker) pollLoop(loopID int) {
	defer w.wg.Done()

	taskTypes := w.getTaskTypeNames()

	for {
		select {
		case <-w.ctx.Done():
			log.Printf("[Worker] Poll loop %d stopping", loopID)
			return
		default:
		}

		w.pool.PollCh() <- &PollRequest{
			NodeID:    w.nodeID,
			TaskTypes: taskTypes,
			TimeoutMs: w.pollTimeoutMs,
		}

		resp := <-w.pool.PollRespCh()

		if resp.Error != "" {
			if status.Code(fmt.Errorf(resp.Error)) == codes.DeadlineExceeded {
				continue
			}
			if w.ctx.Err() != nil {
				return
			}
			log.Printf("[Worker] Poll loop %d error: %s", loopID, resp.Error)
			time.Sleep(time.Second)
			continue
		}

		if resp.Task != nil && resp.Task.TaskId != 0 {
			w.executeAndComplete(resp.Task)
		}
	}
}

func (w *Worker) executeAndComplete(task *pb.TaskRequest) {
	startTime := time.Now()

	log.Printf("[Worker] Received task: id=%d type=%s workflow=%d stage=%d",
		task.TaskId, task.TaskType, task.WorkflowId, task.StageIndex)

	w.tasksMu.RLock()
	def, exists := w.tasks[task.TaskType]
	w.tasksMu.RUnlock()

	var result *pb.TaskResult

	if !exists {
		w.incrementFailed()
		result = &pb.TaskResult{
			TaskId:            task.TaskId,
			WorkflowId:        task.WorkflowId,
			Success:           false,
			Error:             fmt.Sprintf("unknown task type: %s", task.TaskType),
			PublisherRouterId: task.PublisherRouterId,
			PublisherWorkerId: task.PublisherWorkerId,
		}
	} else {
		timeout := time.Duration(task.TimeoutMs) * time.Millisecond
		if timeout <= 0 {
			timeout = def.DefaultTimeout
		}

		ctx, cancel := context.WithTimeout(w.ctx, timeout)
		output, err := def.Handler(ctx, task.Input)
		cancel()

		executionTime := time.Since(startTime)

		if err != nil {
			w.incrementFailed()
			log.Printf("[Worker] Task %d failed: %v (took %v)", task.TaskId, err, executionTime)
			result = &pb.TaskResult{
				TaskId:            task.TaskId,
				WorkflowId:        task.WorkflowId,
				Success:           false,
				Error:             err.Error(),
				ExecutionTimeMs:   executionTime.Milliseconds(),
				PublisherRouterId: task.PublisherRouterId,
				PublisherWorkerId: task.PublisherWorkerId,
			}
		} else {
			w.incrementProcessed()
			log.Printf("[Worker] Task %d completed successfully (took %v)", task.TaskId, executionTime)
			result = &pb.TaskResult{
				TaskId:            task.TaskId,
				WorkflowId:        task.WorkflowId,
				Success:           true,
				Output:            output,
				ExecutionTimeMs:   executionTime.Milliseconds(),
				PublisherRouterId: task.PublisherRouterId,
				PublisherWorkerId: task.PublisherWorkerId,
			}
		}
	}

	ctx, cancel := context.WithTimeout(w.ctx, 10*time.Second)
	resp, err := w.client.CompleteTask(ctx, result)
	cancel()

	if err != nil {
		log.Printf("[Worker] Failed to send result for task %d: %v", task.TaskId, err)
	} else if !resp.Success {
		log.Printf("[Worker] Server rejected result for task %d: %s", task.TaskId, resp.Message)
	}
}

func (w *Worker) ExecuteWorkflow(ctx context.Context, workflowFn WorkflowFunc, opts ...WorkflowOption) error {
	options := &WorkflowOptions{}
	for _, opt := range opts {
		opt(options)
	}

	startResp, err := w.client.StartLiveWorkflow(ctx, &pb.StartLiveWorkflowRequest{
		NodeId:          w.nodeID,
		FailureCallback: options.FailureCallback,
		WorkflowName:    options.WorkflowName,
		TimeoutMs:       options.TimeoutMs,
	})
	if err != nil {
		return fmt.Errorf("failed to start live workflow: %w", err)
	}
	if !startResp.Success {
		return fmt.Errorf("failed to start live workflow: %s", startResp.Error)
	}

	workflowID := startResp.WorkflowId

	wfCtx := &Context{
		pool:       w.pool,
		workflowID: workflowID,
		nodeID:     w.nodeID,
		ctx:        ctx,
	}

	wfErr := workflowFn(wfCtx)

	_, completeErr := w.client.CompleteWorkflow(ctx, &pb.CompleteWorkflowRequest{
		WorkflowId: workflowID,
		Success:    wfErr == nil,
		Error:      errorString(wfErr),
	})

	if wfErr != nil {
		return wfErr
	}
	if completeErr != nil {
		return fmt.Errorf("failed to signal workflow completion: %w", completeErr)
	}

	return nil
}

func (w *Worker) ExecuteWorkflowWithID(ctx context.Context, workflowID int64, workflowFn WorkflowFunc) error {
	wfCtx := &Context{
		pool:       w.pool,
		workflowID: workflowID,
		nodeID:     w.nodeID,
		ctx:        ctx,
	}

	wfErr := workflowFn(wfCtx)

	_, completeErr := w.client.CompleteWorkflow(ctx, &pb.CompleteWorkflowRequest{
		WorkflowId: workflowID,
		Success:    wfErr == nil,
		Error:      errorString(wfErr),
	})

	if wfErr != nil {
		return wfErr
	}
	if completeErr != nil {
		return fmt.Errorf("failed to signal workflow completion: %w", completeErr)
	}

	return nil
}

func (w *Worker) Stop() error {
	log.Printf("[Worker] Stopping...")

	w.cancel()
	w.wg.Wait()

	if w.client != nil && w.nodeID != 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := w.client.UnregisterWorker(ctx, &pb.UnregisterWorkerRequest{
			NodeId: w.nodeID,
		})
		cancel()
		if err != nil {
			log.Printf("[Worker] Failed to unregister: %v", err)
		} else {
			log.Printf("[Worker] Unregistered from server")
		}
	}

	if w.pool != nil {
		w.pool.Stop()
	}

	if w.conn != nil {
		w.conn.Close()
	}

	log.Printf("[Worker] Stopped")
	return nil
}

func (w *Worker) WaitForShutdown() {
	<-w.ctx.Done()
	w.wg.Wait()
}

func (w *Worker) NodeID() int64 {
	return w.nodeID
}

func (w *Worker) getTaskTypeNames() []string {
	w.tasksMu.RLock()
	defer w.tasksMu.RUnlock()

	names := make([]string, 0, len(w.tasks))
	for name := range w.tasks {
		names = append(names, name)
	}
	return names
}

func (w *Worker) incrementProcessed() {
	w.metricsMu.Lock()
	w.tasksProcessed++
	w.metricsMu.Unlock()
}

func (w *Worker) incrementFailed() {
	w.metricsMu.Lock()
	w.tasksFailed++
	w.metricsMu.Unlock()
}

func (w *Worker) GetMetrics() (processed, failed int64) {
	w.metricsMu.RLock()
	defer w.metricsMu.RUnlock()
	return w.tasksProcessed, w.tasksFailed
}

func (w *Worker) startFailureSubscription() {
	w.failureMu.RLock()
	hasCallback := w.failureCallback != nil
	w.failureMu.RUnlock()

	if !hasCallback {
		return
	}

	w.wg.Add(1)
	go w.failureSubscriptionLoop()
}

func (w *Worker) failureSubscriptionLoop() {
	defer w.wg.Done()

	retryDelay := time.Second

	for {
		select {
		case <-w.ctx.Done():
			log.Printf("[Worker] Failure subscription stopping")
			return
		default:
		}

		stream, err := w.client.SubscribeFailures(w.ctx, &pb.SubscribeFailuresRequest{
			NodeId: w.nodeID,
		})
		if err != nil {
			if w.ctx.Err() != nil {
				return
			}
			log.Printf("[Worker] Failed to subscribe to failures: %v, retrying in %v", err, retryDelay)
			select {
			case <-time.After(retryDelay):
			case <-w.ctx.Done():
				return
			}
			continue
		}

		log.Printf("[Worker] Subscribed to failure notifications")

		for {
			notification, err := stream.Recv()
			if err != nil {
				if w.ctx.Err() != nil {
					return
				}
				log.Printf("[Worker] Failure subscription stream error: %v, reconnecting...", err)
				break
			}

			log.Printf("[Worker] Received failure notification: workflow_id=%d task_id=%d type=%s error=%s",
				notification.WorkflowId, notification.TaskId, notification.FailureType, notification.Error)

			w.failureMu.RLock()
			callback := w.failureCallback
			w.failureMu.RUnlock()

			if callback != nil {
				go callback(notification)
			}
		}
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}