package tree

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
)

// Lock 获取 task 级文件锁，返回 unlock 函数。
// 所有写操作（NewTask/Save/Create/Complete/Rollback/Branch/Merge）必须加锁。
func Lock(taskDir string) (unlock func(), err error) {
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir task dir: %w", err)
	}

	lockPath := filepath.Join(taskDir, "task.lock")
	fl := flock.New(lockPath)

	if err := fl.Lock(); err != nil {
		return nil, fmt.Errorf("acquire lock: %w", err)
	}

	return func() {
		fl.Unlock()
	}, nil
}
