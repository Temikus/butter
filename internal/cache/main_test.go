package cache

import (
	"context"
	"os"
	"testing"

	"github.com/redis/go-redis/v9"
)

// discardLogger swallows go-redis's internal log output.
type discardLogger struct{}

func (discardLogger) Printf(context.Context, string, ...any) {}

// TestMain silences go-redis's internal pool logger for this test binary.
// TestNewRedis_ConnectionFailure intentionally dials an unreachable address, and
// the pool logs every failed dial attempt to stderr before PING gives up. The
// logger is process-global, which is fine here: it only affects the test binary.
func TestMain(m *testing.M) {
	redis.SetLogger(discardLogger{})
	os.Exit(m.Run())
}
