package reqid

import (
	"context"
	"crypto/rand"
	"fmt"
)

type ctxKey struct{}

func New() string {
	var b [4]byte
	rand.Read(b[:]) //nolint:errcheck
	return fmt.Sprintf("%08x", b)
}

func With(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

func From(ctx context.Context) string {
	if id, ok := ctx.Value(ctxKey{}).(string); ok {
		return id
	}
	return "????????"
}
