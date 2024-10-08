package notification

import (
	"context"
)

type State int

const (
	StateUnknown State = iota
	StateNoDrift
	StateExtraWorkspaceInRemote
	StateMissingWorkspaceInRemote
)

type Location struct {
	Directory string
	Workspace string
}

type Notification interface {
	ExtraWorkspaceInRemote(ctx context.Context, dir string, workspace string) error
	MissingWorkspaceInRemote(ctx context.Context, dir string, workspace string) error
	PlanDrift(ctx context.Context, dir string, workspace string, cliffnote string) error
	WorkspaceDriftSummary(ctx context.Context, workspacesDrifted int32, workspacesUndrifted int32, totalWorkspaces int32) error
	// TemporaryError is called when an error occurs but we can't really tell what it means
	TemporaryError(ctx context.Context, dir string, workspace string, err error) error
}
