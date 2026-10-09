package core

import (
	"context"
)

// Checker is implemented by any plugin that can verify its config/credentials.
// Used by `heinzel config check`.
type Checker interface {
	Check(ctx context.Context) error
}
