package launcher

func reportProgress(reporter ProgressReporter, event ProgressEvent) {
	if reporter != nil {
		reporter.Report(event)
	}
}
