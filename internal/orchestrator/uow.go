package orchestrator

import "github.com/ndzuki/release-manager/internal/store"

// CreateOperationRequest is the orchestrator-facing operation creation input.
type CreateOperationRequest = store.OperationCreationRequest

// CreateOperationResult is the orchestrator-facing operation creation result.
type CreateOperationResult = store.OperationCreationResult

// OperationCreationUnitOfWork commits standard operation creation atomically.
type OperationCreationUnitOfWork = store.OperationCreationUnitOfWork
