package orchestrator

// SetBeforeRegister makes f run between a result's miss-load and the
// registration of the loaded execution. It returns a func that restores the
// previous hook.
func SetBeforeRegister(f func()) (restore func()) {
	prev := beforeRegister
	beforeRegister = f
	return func() { beforeRegister = prev }
}
