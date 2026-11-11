package go2rtc

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// supervisor keeps one go2rtc process running, restarting it with backoff
// when it exits unexpectedly, and stops it cleanly on request.
type supervisor struct {
	bin        string
	args       []string
	logger     *slog.Logger
	minBackoff time.Duration
	maxBackoff time.Duration
	// stopGrace is how long to wait after SIGTERM before SIGKILL.
	stopGrace time.Duration

	mu      sync.Mutex
	cmd     *exec.Cmd
	stopped chan struct{} // closed when the current process exits
	quit    chan struct{} // closed by Stop to end the run loop
	done    chan struct{} // closed when the run loop has returned
	running bool
}

func newSupervisor(bin string, args []string, logger *slog.Logger) *supervisor {
	return &supervisor{
		bin:        bin,
		args:       args,
		logger:     logger,
		minBackoff: 500 * time.Millisecond,
		maxBackoff: 30 * time.Second,
		stopGrace:  5 * time.Second,
	}
}

// Start launches the process and the restart loop. It returns once the
// process has been started (not once it is ready to serve).
func (s *supervisor) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return errors.New("supervisor already running")
	}
	s.quit = make(chan struct{})
	s.done = make(chan struct{})
	if err := s.launchLocked(); err != nil {
		return err
	}
	s.running = true
	go s.loop()
	return nil
}

func (s *supervisor) launchLocked() error {
	cmd := exec.Command(s.bin, s.args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", s.bin, err)
	}
	s.logger.Info("engine started", "pid", cmd.Process.Pid)
	stopped := make(chan struct{})
	go forwardLogs(s.logger, "go2rtc", stdout)
	go forwardLogs(s.logger, "go2rtc", stderr)
	go func() {
		err := cmd.Wait()
		s.logger.Info("engine exited", "pid", cmd.Process.Pid, "err", err)
		close(stopped)
	}()
	s.cmd = cmd
	s.stopped = stopped
	return nil
}

// loop restarts the process after unexpected exits until quit is closed.
func (s *supervisor) loop() {
	defer close(s.done)
	backoff := s.minBackoff
	for {
		s.mu.Lock()
		stopped := s.stopped
		s.mu.Unlock()

		started := time.Now()
		select {
		case <-s.quit:
			return
		case <-stopped:
		}
		// Ran long enough to count as healthy: reset backoff.
		if time.Since(started) > s.maxBackoff {
			backoff = s.minBackoff
		}
		s.logger.Warn("engine exited unexpectedly, restarting", "backoff", backoff)
		select {
		case <-s.quit:
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, s.maxBackoff)

		s.mu.Lock()
		err := s.launchLocked()
		s.mu.Unlock()
		if err != nil {
			s.logger.Error("engine restart failed", "err", err)
			// Fake a stopped channel so the loop keeps retrying.
			s.mu.Lock()
			ch := make(chan struct{})
			close(ch)
			s.stopped = ch
			s.mu.Unlock()
		}
	}
}

// Stop ends the restart loop and terminates the process (SIGTERM, then
// SIGKILL after stopGrace). It is safe to call when not running.
func (s *supervisor) Stop(ctx context.Context) error {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return nil
	}
	s.running = false
	close(s.quit)
	cmd, stopped, done := s.cmd, s.stopped, s.done
	s.mu.Unlock()

	<-done
	select {
	case <-stopped:
		return nil // already gone
	default:
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("signal engine: %w", err)
	}
	select {
	case <-stopped:
		return nil
	case <-time.After(s.stopGrace):
		s.logger.Warn("engine ignored SIGTERM, killing")
		_ = cmd.Process.Kill()
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		return ctx.Err()
	}
	<-stopped
	return nil
}

// Restart stops the current process and starts a new one with the same
// arguments (used after the config file changed).
func (s *supervisor) Restart(ctx context.Context) error {
	if err := s.Stop(ctx); err != nil {
		return err
	}
	return s.Start()
}

// Alive reports whether the process is currently running.
func (s *supervisor) Alive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running || s.stopped == nil {
		return false
	}
	select {
	case <-s.stopped:
		return false
	default:
		return true
	}
}

// forwardLogs copies the child's output lines into our structured log.
func forwardLogs(logger *slog.Logger, name string, r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		logger.Info(name, "line", sc.Text())
	}
}
