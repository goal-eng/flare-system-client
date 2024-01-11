package clients

import (
	"strings"
	"time"

	"flare-tlc/logger"
)

const (
	ListenerInterval time.Duration = 2 * time.Second // TODO: change to 10 seconds or read from config
	MaxTxSendRetries int           = 1
	TxRetryInterval  time.Duration = 5 * time.Second
)

type ExecuteStatus struct {
	Success bool
	Message string
}

func ExecuteWithRetry(f func() error, maxRetries int) <-chan ExecuteStatus {
	out := make(chan ExecuteStatus)
	go func() {
		for ri := 0; ri < maxRetries; ri++ {
			err := f()
			if err == nil {
				out <- ExecuteStatus{Success: true}
				return
			} else {
				logger.Error("error executing in retry no. %d: %v", ri, err)
			}
			time.Sleep(TxRetryInterval)
		}
		out <- ExecuteStatus{Success: false, Message: "max retries reached"}
	}()
	return out
}

// ExistsAsSubstring returns true if any of the strings in the slice is a substring of s
func ExistsAsSubstring(slice []string, s string) bool {
	for _, item := range slice {
		if strings.Contains(s, item) {
			return true
		}
	}
	return false
}
