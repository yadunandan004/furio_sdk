package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"furio_sdk/workflow"
)

func (s *Server) handleSubmitWorkflow(w http.ResponseWriter, r *http.Request) {
	var req SubmitWorkflowRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, SubmitWorkflowResponse{
			Success: false,
			Error:   "invalid request body: " + err.Error(),
		})
		return
	}

	if len(req.Tasks) == 0 {
		writeJSON(w, http.StatusBadRequest, SubmitWorkflowResponse{
			Success: false,
			Error:   "tasks array cannot be empty",
		})
		return
	}

	timeout := 120 * time.Second
	if req.TimeoutMs > 0 {
		timeout = time.Duration(req.TimeoutMs) * time.Millisecond
	}

	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	startTime := time.Now()
	var workflowID int64
	var taskResults []TaskResultResponse

	opts := []workflow.WorkflowOption{
		workflow.WithWorkflowName(req.Name),
		workflow.WithWorkflowTimeout(int32(timeout.Milliseconds())),
	}

	err := s.worker.ExecuteWorkflow(ctx, func(wfCtx *workflow.Context) error {
		workflowID = wfCtx.WorkflowID()

		for i, task := range req.Tasks {
			taskTimeout := int32(60000)
			if task.TimeoutMs > 0 {
				taskTimeout = task.TimeoutMs
			}

			output, err := wfCtx.ExecuteTaskWithTimeout(task.TaskType, task.Input, taskTimeout)

			result := TaskResultResponse{
				TaskID:  int64(i + 1),
				Success: err == nil,
				Output:  output,
			}
			if err != nil {
				result.Error = err.Error()
			}
			taskResults = append(taskResults, result)

			if err != nil {
				return err
			}
		}
		return nil
	}, opts...)

	duration := time.Since(startTime)

	if err != nil {
		writeJSON(w, http.StatusOK, WorkflowResultResponse{
			WorkflowID: workflowID,
			Success:    false,
			Tasks:      taskResults,
			Error:      err.Error(),
			DurationMs: duration.Milliseconds(),
		})
		return
	}

	writeJSON(w, http.StatusOK, WorkflowResultResponse{
		WorkflowID: workflowID,
		Success:    true,
		Tasks:      taskResults,
		DurationMs: duration.Milliseconds(),
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, HealthResponse{
		Status:        "healthy",
		WorkerID:      s.workerID,
		NodeID:        s.worker.NodeID(),
		UptimeSeconds: int64(s.Uptime().Seconds()),
	})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	processed, failed := s.worker.GetMetrics()

	writeJSON(w, http.StatusOK, MetricsResponse{
		WorkerID:       s.workerID,
		NodeID:         s.worker.NodeID(),
		TasksProcessed: processed,
		TasksFailed:    failed,
		UptimeSeconds:  int64(s.Uptime().Seconds()),
	})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
