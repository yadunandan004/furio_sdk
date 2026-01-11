package workflow

import (
	"context"
	"sync"
	"time"

	pb "furio_sdk/proto"
)

type SubmitRequest struct {
	WorkflowID int64
	NodeID     int64
	TaskType   string
	Input      []byte
	TimeoutMs  int32
	StageIndex int32
}

type SubmitResponse struct {
	TaskID  int64
	Success bool
	Error   string
}

type ResultRequest struct {
	WorkflowID int64
	TaskID     int64
	TimeoutMs  int32
}

type ResultResponse struct {
	TaskID  int64
	Ready   bool
	Success bool
	Output  []byte
	Error   string
}

type PollRequest struct {
	NodeID    int64
	TaskTypes []string
	TimeoutMs int32
}

type PollResponse struct {
	Task  *pb.TaskRequest
	Error string
}

type TaskPool struct {
	client       pb.WorkerServiceClient
	submitCh     chan []*SubmitRequest
	resultCh     chan []*ResultRequest
	pollCh       chan *PollRequest
	submitRespCh chan []*SubmitResponse
	resultRespCh chan []*ResultResponse
	pollRespCh   chan *PollResponse
	errCh        chan error
	stopCh       chan struct{}
	wg           sync.WaitGroup
	config       PoolConfig
}

type PoolConfig struct {
	SubmitWorkers int
	ResultWorkers int
	PollWorkers   int
	BufferSize    int
}

func NewTaskPool(client pb.WorkerServiceClient, cfg PoolConfig) *TaskPool {
	if cfg.SubmitWorkers == 0 {
		cfg.SubmitWorkers = 4
	}
	if cfg.ResultWorkers == 0 {
		cfg.ResultWorkers = 8
	}
	if cfg.PollWorkers == 0 {
		cfg.PollWorkers = 10
	}
	if cfg.BufferSize == 0 {
		cfg.BufferSize = 100
	}

	return &TaskPool{
		client:       client,
		submitCh:     make(chan []*SubmitRequest, cfg.BufferSize),
		resultCh:     make(chan []*ResultRequest, cfg.BufferSize),
		pollCh:       make(chan *PollRequest, cfg.BufferSize),
		submitRespCh: make(chan []*SubmitResponse, cfg.BufferSize),
		resultRespCh: make(chan []*ResultResponse, cfg.BufferSize),
		pollRespCh:   make(chan *PollResponse, cfg.BufferSize),
		errCh:        make(chan error, cfg.BufferSize),
		stopCh:       make(chan struct{}),
		config:       cfg,
	}
}

func (p *TaskPool) Start() {
	for i := 0; i < p.config.SubmitWorkers; i++ {
		p.wg.Add(1)
		go p.submitWorker()
	}
	for i := 0; i < p.config.ResultWorkers; i++ {
		p.wg.Add(1)
		go p.resultWorker()
	}
	for i := 0; i < p.config.PollWorkers; i++ {
		p.wg.Add(1)
		go p.pollWorker()
	}
}

func (p *TaskPool) Stop() {
	close(p.stopCh)
	p.wg.Wait()
	close(p.submitRespCh)
	close(p.resultRespCh)
	close(p.pollRespCh)
	close(p.errCh)
}

func (p *TaskPool) submitWorker() {
	defer p.wg.Done()
	for {
		select {
		case <-p.stopCh:
			return
		case batch := <-p.submitCh:
			if len(batch) == 0 {
				continue
			}

			tasks := make([]*pb.TaskSubmit, len(batch))
			var workflowID, nodeID int64
			for i, req := range batch {
				if i == 0 {
					workflowID = req.WorkflowID
					nodeID = req.NodeID
				}
				tasks[i] = &pb.TaskSubmit{
					TaskType:   req.TaskType,
					Input:      req.Input,
					TimeoutMs:  req.TimeoutMs,
					StageIndex: req.StageIndex,
				}
			}

			resp, err := p.client.ExecuteTasks(context.Background(), &pb.ExecuteTasksRequest{
				WorkflowId: workflowID,
				NodeId:     nodeID,
				Tasks:      tasks,
			})

			if err != nil {
				p.errCh <- err
				results := make([]*SubmitResponse, len(batch))
				for i := range results {
					results[i] = &SubmitResponse{Success: false, Error: err.Error()}
				}
				p.submitRespCh <- results
				continue
			}

			results := make([]*SubmitResponse, len(batch))
			for i := range batch {
				if i < len(resp.Results) {
					results[i] = &SubmitResponse{
						TaskID:  resp.Results[i].TaskId,
						Success: resp.Results[i].Success,
						Error:   resp.Results[i].Error,
					}
				} else {
					results[i] = &SubmitResponse{Success: false, Error: "missing result"}
				}
			}
			p.submitRespCh <- results
		}
	}
}

func (p *TaskPool) resultWorker() {
	defer p.wg.Done()
	for {
		select {
		case <-p.stopCh:
			return
		case batch := <-p.resultCh:
			if len(batch) == 0 {
				continue
			}

			queries := make([]*pb.TaskResultQuery, len(batch))
			var workflowID int64
			var timeoutMs int32
			for i, req := range batch {
				if i == 0 {
					workflowID = req.WorkflowID
					timeoutMs = req.TimeoutMs
				}
				queries[i] = &pb.TaskResultQuery{
					TaskId: req.TaskID,
				}
			}

			resp, err := p.client.GetTaskResults(context.Background(), &pb.GetTaskResultsRequest{
				WorkflowId: workflowID,
				TimeoutMs:  timeoutMs,
				Tasks:      queries,
			})

			if err != nil {
				p.errCh <- err
				results := make([]*ResultResponse, len(batch))
				for i, req := range batch {
					results[i] = &ResultResponse{TaskID: req.TaskID, Ready: false, Error: err.Error()}
				}
				p.resultRespCh <- results
				continue
			}

			results := make([]*ResultResponse, len(batch))
			for i, req := range batch {
				if i < len(resp.Results) {
					results[i] = &ResultResponse{
						TaskID:  resp.Results[i].TaskId,
						Ready:   resp.Results[i].Ready,
						Success: resp.Results[i].Success,
						Output:  resp.Results[i].Output,
						Error:   resp.Results[i].Error,
					}
				} else {
					results[i] = &ResultResponse{TaskID: req.TaskID, Ready: false, Error: "missing result"}
				}
			}
			p.resultRespCh <- results
		}
	}
}

func (p *TaskPool) pollWorker() {
	defer p.wg.Done()
	for {
		select {
		case <-p.stopCh:
			return
		case req := <-p.pollCh:
			if req == nil {
				continue
			}

			ctx, cancel := context.WithTimeout(context.Background(), time.Duration(req.TimeoutMs+5000)*time.Millisecond)
			task, err := p.client.PollTask(ctx, &pb.PollTaskRequest{
				NodeId:    req.NodeID,
				TaskTypes: req.TaskTypes,
				TimeoutMs: req.TimeoutMs,
			})
			cancel()

			if err != nil {
				p.pollRespCh <- &PollResponse{Error: err.Error()}
				continue
			}

			p.pollRespCh <- &PollResponse{Task: task}
		}
	}
}

func (p *TaskPool) SubmitCh() chan<- []*SubmitRequest {
	return p.submitCh
}

func (p *TaskPool) ResultCh() chan<- []*ResultRequest {
	return p.resultCh
}

func (p *TaskPool) PollCh() chan<- *PollRequest {
	return p.pollCh
}

func (p *TaskPool) SubmitRespCh() <-chan []*SubmitResponse {
	return p.submitRespCh
}

func (p *TaskPool) ResultRespCh() <-chan []*ResultResponse {
	return p.resultRespCh
}

func (p *TaskPool) PollRespCh() <-chan *PollResponse {
	return p.pollRespCh
}

func (p *TaskPool) Errors() <-chan error {
	return p.errCh
}