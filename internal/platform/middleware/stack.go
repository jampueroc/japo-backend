package middleware

import "runtime"

// maxStackSize bounds the stack trace we keep on a panic: on a small box we
// do not want a runaway allocation in the error path.
const maxStackSize = 8 << 10

func stackTrace() []byte {
	buf := make([]byte, maxStackSize)
	n := runtime.Stack(buf, false)
	return buf[:n]
}
