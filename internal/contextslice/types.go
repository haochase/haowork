package contextslice

import (
	"context"
	"io"

	"github.com/haochase/haowork/internal/model"
)

type SourceReader interface {
	Open(context.Context, string) (io.ReadCloser, error)
}

type ContextRequest struct {
	TaskID          string
	SupersedesID    string
	Reason          string
	Sources         []string
	AllowedPaths    []string
	DeniedPaths     []string
	ExpectedDigests map[string]string
	Actor           model.Actor
}

type IDGenerator interface {
	New(prefix string) (string, error)
}

type ContextBuilder interface {
	Build(context.Context, ContextRequest) (model.ContextSlice, error)
}
