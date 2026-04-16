package api

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
	"github.com/a-matson/workflow-orchestrator/backend/internal/orchestrator"
	"github.com/a-matson/workflow-orchestrator/backend/internal/persistence"
)

// GRPCServer implements OrchestratorServiceServer (generated from orchestrator.proto).
// For full proto-generated code, run: make proto
type GRPCServer struct {
	store        *persistence.Store
	redis        *persistence.RedisClient
	orchestrator *orchestrator.Orchestrator
	hub          *Hub
}

func NewGRPCServer(
	store *persistence.Store,
	redis *persistence.RedisClient,
	orch *orchestrator.Orchestrator,
	hub *Hub,
) *GRPCServer {
	return &GRPCServer{
		store:        store,
		redis:        redis,
		orchestrator: orch,
		hub:          hub,
	}
}

// RegisterGRPC registers the gRPC server on the provided gRPC server instance.
// Call proto-generated RegisterOrchestratorServiceServer(grpcSrv, s) after generating proto code.
func (s *GRPCServer) Register(grpcSrv *grpc.Server) {
	// proto-gen: orchestratorv1.RegisterOrchestratorServiceServer(grpcSrv, s)
	log.Info().Msg("gRPC OrchestratorService registered")
}

// ──────────────────────────────────────────────────────────────
// Workflow definitions
// ──────────────────────────────────────────────────────────────

func (s *GRPCServer) CreateWorkflow(ctx context.Context, def *models.WorkflowDefinition) (*models.WorkflowDefinition, error) {
	if err := s.store.SaveWorkflowDefinition(ctx, def); err != nil {
		return nil, status.Errorf(codes.Internal, "save workflow: %v", err)
	}
	return def, nil
}

func (s *GRPCServer) GetWorkflow(ctx context.Context, id string) (*models.WorkflowDefinition, error) {
	def, err := s.store.GetWorkflowDefinition(ctx, id)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "workflow %s not found", id)
	}
	return def, nil
}

func (s *GRPCServer) ListWorkflows(ctx context.Context) ([]*models.WorkflowDefinition, error) {
	defs, err := s.store.ListWorkflowDefinitions(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list workflows: %v", err)
	}
	return defs, nil
}

// ──────────────────────────────────────────────────────────────
// Execution lifecycle
// ──────────────────────────────────────────────────────────────

func (s *GRPCServer) TriggerWorkflow(ctx context.Context, workflowID string, payload map[string]any) (*models.WorkflowExecution, error) {
	def, err := s.store.GetWorkflowDefinition(ctx, workflowID)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "workflow %s not found", workflowID)
	}

	exec, err := s.orchestrator.StartWorkflow(ctx, def, payload)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "start workflow: %v", err)
	}

	return exec, nil
}

func (s *GRPCServer) GetExecution(ctx context.Context, id string) (*models.WorkflowExecution, error) {
	exec, err := s.store.GetWorkflowExecution(ctx, id)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "execution %s not found", id)
	}
	return exec, nil
}

func (s *GRPCServer) ListExecutions(ctx context.Context, limit, offset int) ([]*models.WorkflowExecution, error) {
	execs, err := s.store.ListWorkflowExecutions(ctx, limit, offset)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list executions: %v", err)
	}
	return execs, nil
}

func (s *GRPCServer) RetryExecution(ctx context.Context, execID string) (*models.WorkflowExecution, error) {
	exec, err := s.store.GetWorkflowExecution(ctx, execID)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "execution %s not found", execID)
	}

	def, err := s.store.GetWorkflowDefinition(ctx, exec.WorkflowID)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "workflow def not found for retry")
	}

	newExec, err := s.orchestrator.StartWorkflow(ctx, def, exec.TriggerPayload)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "retry: %v", err)
	}
	return newExec, nil
}

// ──────────────────────────────────────────────────────────────
// Streaming — WatchExecution (server-streaming)
// ──────────────────────────────────────────────────────────────

// WatchExecution streams real-time execution events back to the gRPC caller.
// In production, implement this using the Hub's broadcast channel.
// Sketch below shows the pattern:
//
//	func (s *GRPCServer) WatchExecution(req *WatchReq, stream OrchestratorService_WatchExecutionServer) error {
//	    eventCh := s.hub.Subscribe(req.ExecutionId)
//	    defer s.hub.Unsubscribe(eventCh)
//	    for {
//	        select {
//	        case evt := <-eventCh:
//	            if err := stream.Send(protoEvent(evt)); err != nil { return err }
//	        case <-stream.Context().Done():
//	            return nil
//	        }
//	    }
//	}

// ──────────────────────────────────────────────────────────────
// Worker protocol — task pickup handshake
// ──────────────────────────────────────────────────────────────

func (s *GRPCServer) ReportTaskStarted(ctx context.Context, taskExecID, workerID string) error {
	if err := s.orchestrator.MarkTaskRunning(ctx, taskExecID, workerID); err != nil {
		return status.Errorf(codes.Internal, "mark task running: %v", err)
	}
	return nil
}

func (s *GRPCServer) ReportTaskCompleted(ctx context.Context, result *models.TaskResult) error {
	if err := s.orchestrator.ProcessResult(ctx, result); err != nil {
		return status.Errorf(codes.Internal, "process result: %v", err)
	}
	return nil
}

// ──────────────────────────────────────────────────────────────
// System
// ──────────────────────────────────────────────────────────────

func (s *GRPCServer) HealthCheck(ctx context.Context) (map[string]string, error) {
	components := map[string]string{"api": "ok"}

	if err := s.redis.Ping(ctx); err != nil {
		components["redis"] = fmt.Sprintf("error: %v", err)
	} else {
		components["redis"] = "ok"
	}

	if s.store != nil {
		components["postgres"] = "ok"
	}

	return components, nil
}

func (s *GRPCServer) GetMetrics(ctx context.Context) (map[string]int64, error) {
	metrics := s.orchestrator.GetMetrics()

	qd, _ := s.redis.QueueDepth(ctx)
	rd, _ := s.redis.RetryQueueDepth(ctx)
	metrics["queue_depth"] = qd
	metrics["retry_queue_depth"] = rd
	metrics["ws_clients"] = int64(s.hub.ConnectedClients())

	return metrics, nil
}

// UnaryInterceptor provides logging, recovery, and timeout for all gRPC calls
func UnaryInterceptor(
	ctx context.Context,
	req any,
	info *grpc.UnaryServerInfo,
	handler grpc.UnaryHandler,
) (any, error) {
	start := time.Now()

	defer func() {
		if r := recover(); r != nil {
			log.Error().
				Interface("panic", r).
				Str("method", info.FullMethod).
				Msg("gRPC handler panic recovered")
		}
	}()

	resp, err := handler(ctx, req)

	log.Info().
		Str("method", info.FullMethod).
		Dur("duration", time.Since(start)).
		Bool("error", err != nil).
		Msg("gRPC call")

	return resp, err
}
