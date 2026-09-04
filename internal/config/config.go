// Package config holds daemon-level settings loaded from verbs.yaml.
package config

import (
	"fmt"
	"strings"
)

// Daemon is the normative daemon{} block from verbs.yaml.
type Daemon struct {
	Listen              string  `yaml:"listen"`
	TaskTimeoutS        int     `yaml:"task_timeout_s"`
	MaxRetries          int     `yaml:"max_retries"`
	BackoffBaseS        float64 `yaml:"backoff_base_s"`
	CBTripThreshold     int     `yaml:"cb_trip_threshold"`
	CBOpenS             int     `yaml:"cb_open_s"`
	MaxQueueDepth       int     `yaml:"max_queue_depth"`
	StreamBufferDefault int     `yaml:"stream_buffer_default"`
}

// Default returns MVP defaults.
func Default() Daemon {
	return Daemon{
		Listen:              "127.0.0.1:8477",
		TaskTimeoutS:        30,
		MaxRetries:          5,
		BackoffBaseS:        1,
		CBTripThreshold:     5,
		CBOpenS:             60,
		MaxQueueDepth:       1024,
		StreamBufferDefault: 128,
	}
}

// Validate checks daemon settings.
func (d Daemon) Validate() error {
	if d.Listen == "" {
		return fmt.Errorf("daemon.listen is required")
	}
	host, port, ok := splitHostPort(d.Listen)
	if !ok || port == "" {
		return fmt.Errorf("daemon.listen must be host:port, got %q", d.Listen)
	}
	if host != "127.0.0.1" && host != "localhost" {
		return fmt.Errorf("daemon.listen must bind loopback 127.0.0.1 only, got host %q", host)
	}
	if d.TaskTimeoutS <= 0 {
		return fmt.Errorf("daemon.task_timeout_s must be > 0")
	}
	if d.MaxRetries < 0 {
		return fmt.Errorf("daemon.max_retries must be >= 0")
	}
	if d.BackoffBaseS <= 0 {
		return fmt.Errorf("daemon.backoff_base_s must be > 0")
	}
	if d.CBTripThreshold <= 0 {
		return fmt.Errorf("daemon.cb_trip_threshold must be > 0")
	}
	if d.CBOpenS <= 0 {
		return fmt.Errorf("daemon.cb_open_s must be > 0")
	}
	if d.MaxQueueDepth <= 0 {
		return fmt.Errorf("daemon.max_queue_depth must be > 0")
	}
	if d.StreamBufferDefault <= 0 {
		return fmt.Errorf("daemon.stream_buffer_default must be > 0")
	}
	return nil
}

func splitHostPort(addr string) (host, port string, ok bool) {
	if strings.Count(addr, ":") != 1 {
		return "", "", false
	}
	i := strings.LastIndexByte(addr, ':')
	return addr[:i], addr[i+1:], true
}
