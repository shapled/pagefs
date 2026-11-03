package helper

func IfElse[T any](cond bool, ifVal T, elseVal T) T {
	if cond {
		return ifVal
	}
	return elseVal
}
