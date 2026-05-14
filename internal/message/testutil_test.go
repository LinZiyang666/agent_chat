package message

import (
	"context"

	"github.com/LinZiyang666/agentchat/internal/store"
)

// nopBundler satisfies store.Bundler for the ingester tests in this
// package that exercise only Attach / Detach (no actual ingestion).
type nopBundler struct{}

func (nopBundler) WithTx(_ context.Context, _ func(store.Bundle) error) error {
	return nil
}
