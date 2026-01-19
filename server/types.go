package server

import "encoding/json"

type SubmitWorkflowRequest struct {
	Name      string        `json:"name,omitempty"`
	Tasks     []TaskRequest `json:"tasks"`
	TimeoutMs int32         `json:"timeout_ms,omitempty"`
}

type TaskRequest struct {
	TaskType  string          `json:"task_type"`
	Input     json.RawMessage `json:"input,omitempty"`
	TimeoutMs int32           `json:"timeout_ms,omitempty"`
}

type SubmitWorkflowResponse struct {
	WorkflowID int64  `json:"workflow_id"`
	Success    bool   `json:"success"`
	Error      string `json:"error,omitempty"`
}

type TaskResultResponse struct {
	TaskID  int64           `json:"task_id"`
	Success bool            `json:"success"`
	Output  json.RawMessage `json:"output,omitempty"`
	Error   string          `json:"error,omitempty"`
}

type WorkflowResultResponse struct {
	WorkflowID int64                `json:"workflow_id"`
	Success    bool                 `json:"success"`
	Tasks      []TaskResultResponse `json:"tasks"`
	Error      string               `json:"error,omitempty"`
	DurationMs int64                `json:"duration_ms"`
}

type HealthResponse struct {
	Status        string `json:"status"`
	WorkerID      string `json:"worker_id"`
	NodeID        int64  `json:"node_id"`
	UptimeSeconds int64  `json:"uptime_seconds"`
}

type MetricsResponse struct {
	WorkerID       string `json:"worker_id"`
	NodeID         int64  `json:"node_id"`
	TasksProcessed int64  `json:"tasks_processed"`
	TasksFailed    int64  `json:"tasks_failed"`
	UptimeSeconds  int64  `json:"uptime_seconds"`
}
