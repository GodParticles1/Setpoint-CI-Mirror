package agent

import "time"

func stopTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

func waitTask(done <-chan error) {
	if done != nil {
		<-done
	}
}
