package notification

import (
	"context"

	"go.uber.org/zap"
)

type Zap struct {
	Logger *zap.Logger
}

func (I *Zap) TemporaryError(_ context.Context, dir string, workspace string, err error) error {
	I.Logger.Error("Unknown error in remote", zap.String("dir", dir), zap.String("workspace", workspace), zap.Error(err))
	return nil
}

func (I *Zap) PlanDrift(_ context.Context, dir string, workspace string, cliffnote string) error {
	I.Logger.Info("Plan has drifted", zap.String("dir", dir), zap.String("workspace", workspace), zap.String("cliffnote", cliffnote))
	return nil
}

func (I *Zap) ExtraWorkspaceInRemote(_ context.Context, dir string, workspace string) error {
	I.Logger.Info("Extra workspace in remote", zap.String("dir", dir), zap.String("workspace", workspace))
	return nil
}

func (I *Zap) MissingWorkspaceInRemote(_ context.Context, dir string, workspace string) error {
	I.Logger.Info("Missing workspace in remote", zap.String("dir", dir), zap.String("workspace", workspace))
	return nil
}

func (i *Zap) WorkspaceDriftSummary(_ context.Context, _ int32) error {
	return nil
}

var _ Notification = &Zap{}
