package components

// toastKind is a toast's state as a Status kind: a confirmation, a failure,
// or something now running.
func toastKind(kind string) string {
	switch kind {
	case "fail":
		return "failed"
	case "run":
		return "running"
	}
	return "success"
}

func boolText(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
