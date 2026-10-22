package keyshop

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
)

const (
	ringCap          = 500
	tokenMin         = 20
	tokenMax         = 4096
	nameMax          = 64
	shutdownGrace    = 5 * time.Second
	backoffCap       = 60 * time.Second
	uptimeResetAfter = 60 * time.Second
)

var tryCloudflareURL = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)

// commandFactory builds a child. Tests inject a fake script.
type commandFactory func(ctx context.Context, bin string, args []string, env []string, dir string) *exec.Cmd

// TunnelManagerConfig selects the binary and whether spawning is allowed.
type TunnelManagerConfig struct {
	CloudflaredPath string
	Enabled         *bool
	DataDir         string
	TargetURL       string
	Command         commandFactory
	Now             func() time.Time
	// ProcRoot overrides /proc for ownership checks. Tests point it at a temp dir.
	ProcRoot string
	// Grace overrides the SIGTERM wait. Tests use a short value.
	Grace time.Duration
	// Sleep overrides restart backoff. Tests return immediately.
	Sleep func(time.Duration)
}

// TunnelView is the API shape. Token is never included.
type TunnelView struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Kind      string     `json:"kind"`
	TargetURL string     `json:"target_url"`
	HasToken  bool       `json:"has_token"`
	TokenHint string     `json:"token_hint"`
	Autostart bool       `json:"autostart"`
	Desired   string     `json:"desired"`
	Status    string     `json:"status"`
	PID       int        `json:"pid"`
	PublicURL string     `json:"public_url"`
	LastError string     `json:"last_error"`
	Restarts  int        `json:"restarts"`
	StartedAt *time.Time `json:"started_at"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

type runtimeState struct {
	status    string
	restarts  int
	startedAt time.Time
	cmd       *exec.Cmd
	pid       int
	cancel    context.CancelFunc
	done      chan struct{}
	lines     []string
	stopping  bool
	adopted   bool
}

// TunnelManager owns children the plugin spawned.
type TunnelManager struct {
	store   *Store
	cfg     TunnelManagerConfig
	mu      sync.Mutex
	live    map[string]*runtimeState
	closed  bool
	signals []os.Signal // recorded signals in tests when SignalHook is set
	hook    func(pid int, sig os.Signal) error
}

func NewTunnelManager(store *Store, cfg TunnelManagerConfig) *TunnelManager {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Command == nil {
		cfg.Command = defaultCommand
	}
	if cfg.Grace <= 0 {
		cfg.Grace = shutdownGrace
	}
	if cfg.Sleep == nil {
		cfg.Sleep = time.Sleep
	}
	if cfg.ProcRoot == "" {
		cfg.ProcRoot = "/proc"
	}
	if strings.TrimSpace(cfg.DataDir) == "" && store != nil {
		cfg.DataDir = store.DataDir()
	}
	return &TunnelManager{store: store, cfg: cfg, live: map[string]*runtimeState{}}
}

func defaultCommand(ctx context.Context, bin string, args []string, env []string, dir string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = env
	cmd.Dir = dir
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = shutdownGrace
	return cmd
}

func (m *TunnelManager) Enabled() bool {
	if m == nil || m.cfg.Enabled == nil {
		return true
	}
	return *m.cfg.Enabled
}

// SetSignalHook replaces process signaling. Tests record calls and never signal a real pid.
func (m *TunnelManager) SetSignalHook(hook func(pid int, sig os.Signal) error) {
	m.mu.Lock()
	m.hook = hook
	m.mu.Unlock()
}

func (m *TunnelManager) signalOwned(id string, proc *os.Process, sig os.Signal, adopted bool) error {
	if proc == nil || proc.Pid <= 0 {
		return nil
	}
	// Spawned children are *exec.Cmd processes this manager started. Adopted pids
	// are re-checked immediately before the signal and skipped when the marker is gone.
	if adopted && !ownsPID(m.cfg.ProcRoot, proc.Pid, id) {
		return nil
	}
	m.mu.Lock()
	hook := m.hook
	m.mu.Unlock()
	if hook != nil {
		return hook(proc.Pid, sig)
	}
	return proc.Signal(sig)
}

// Reconcile starts rows that should be running after the ownership check.
func (m *TunnelManager) Reconcile() {
	if m == nil || !m.Enabled() || m.store == nil {
		return
	}
	rows, err := m.store.ListTunnels()
	if err != nil {
		return
	}
	for _, row := range rows {
		owned := false
		if row.PID > 0 {
			owned = m.adoptOrClear(row)
		}
		if owned {
			continue
		}
		if row.Desired == "running" || row.Autostart {
			_ = m.Start(row.ID)
		}
	}
}

// adoptOrClear tracks a pid this plugin still owns. Anything else is cleared and never signaled.
// owned is true only when the pid was adopted, so the caller must not start a second child.
func (m *TunnelManager) adoptOrClear(row TunnelRow) bool {
	if !ownsPID(m.cfg.ProcRoot, row.PID, row.ID) {
		_ = m.store.SetTunnelRuntime(row.ID, row.Desired, 0, row.PublicURL, row.LastError)
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return true
	}
	if st := m.live[row.ID]; st != nil && (st.pid == row.PID || (st.cmd != nil && st.cmd.Process != nil && st.cmd.Process.Pid == row.PID)) {
		return true
	}
	m.live[row.ID] = &runtimeState{
		status:  "running",
		pid:     row.PID,
		adopted: true,
	}
	return true
}

func ownsPID(procRoot string, pid int, id string) bool {
	// On non-Linux hosts a recorded pid is never owned, so nothing is signaled.
	// Tests may pass an absolute fake proc root that is not the real /proc.
	if runtime.GOOS != "linux" && (procRoot == "" || procRoot == "/proc" || !filepath.IsAbs(procRoot)) {
		return false
	}
	path := filepath.Join(procRoot, fmt.Sprintf("%d", pid), "environ")
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	needle := "KEYSHOP_TUNNEL_ID=" + id
	for _, part := range bytes.Split(raw, []byte{0}) {
		if string(part) == needle {
			return true
		}
	}
	return false
}

// Shutdown stops every child this manager started. It never scans the process table.
func (m *TunnelManager) Shutdown() {
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	ids := make([]string, 0, len(m.live))
	for id, st := range m.live {
		if st == nil {
			continue
		}
		if st.adopted && st.pid > 0 || st.cmd != nil && st.cmd.Process != nil {
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			m.stopChild(id, false)
		}(id)
	}
	wg.Wait()
}

func (m *TunnelManager) View(row TunnelRow) TunnelView {
	m.mu.Lock()
	st := m.live[row.ID]
	status := "stopped"
	restarts := 0
	var started *time.Time
	pid := row.PID
	publicURL := row.PublicURL
	if st != nil {
		status = st.status
		restarts = st.restarts
		if !st.startedAt.IsZero() {
			t := st.startedAt
			started = &t
		}
		if st.cmd != nil && st.cmd.Process != nil && status == "running" {
			pid = st.cmd.Process.Pid
		} else if st.pid > 0 && status == "running" {
			pid = st.pid
		}
	} else if row.Desired == "running" {
		status = "stopped"
	}
	m.mu.Unlock()
	return TunnelView{
		ID: row.ID, Name: row.Name, Kind: row.Kind, TargetURL: row.TargetURL,
		HasToken: row.Token != "", TokenHint: tokenHint(row.Token), Autostart: row.Autostart,
		Desired: row.Desired, Status: status, PID: pid, PublicURL: publicURL, LastError: row.LastError,
		Restarts: restarts, StartedAt: started, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func tokenHint(token string) string {
	if token == "" {
		return ""
	}
	if len(token) <= 4 {
		return token
	}
	return token[len(token)-4:]
}

func (m *TunnelManager) Logs(id string, lines int) []string {
	if lines <= 0 {
		lines = 200
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.live[id]
	if st == nil || len(st.lines) == 0 {
		return []string{}
	}
	src := st.lines
	if len(src) > lines {
		src = src[len(src)-lines:]
	}
	out := make([]string, len(src))
	copy(out, src)
	return out
}

// Start spawns the child for id. Already running returns ErrAlreadyRunning.
func (m *TunnelManager) Start(id string) error {
	if !m.Enabled() {
		return errTunnelsDisabled
	}
	row, err := m.store.GetTunnel(id)
	if err != nil {
		return err
	}
	bin, err := m.lookupBinary()
	if err != nil {
		return err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errors.New("tunnel manager stopped")
	}
	if st := m.live[id]; st != nil && (st.status == "running" || st.status == "starting") && !st.stopping {
		m.mu.Unlock()
		return ErrAlreadyRunning
	}
	ctx, cancel := context.WithCancel(context.Background())
	st := &runtimeState{status: "starting", cancel: cancel, done: make(chan struct{})}
	if prev := m.live[id]; prev != nil {
		st.lines = prev.lines
		st.restarts = prev.restarts
	}
	m.live[id] = st
	m.mu.Unlock()
	row.Desired = "running"
	if err := m.store.SetTunnelRuntime(id, "running", row.PID, row.PublicURL, ""); err != nil {
		cancel()
		return err
	}
	go m.supervise(ctx, row, bin, st, false)
	return nil
}

var (
	ErrAlreadyRunning     = errors.New("already running")
	ErrCloudflaredMissing = errors.New("cloudflared not found")
	errTunnelsDisabled    = errors.New("tunnels disabled")
)

func (m *TunnelManager) lookupBinary() (string, error) {
	path := strings.TrimSpace(m.cfg.CloudflaredPath)
	if path == "" {
		path = "cloudflared"
	}
	if strings.Contains(path, string(os.PathSeparator)) || strings.Contains(path, "/") {
		if _, err := os.Stat(path); err != nil {
			return "", ErrCloudflaredMissing
		}
		return path, nil
	}
	found, err := exec.LookPath(path)
	if err != nil {
		return "", ErrCloudflaredMissing
	}
	return found, nil
}

func (m *TunnelManager) BinaryInfo() (found bool, path, version string) {
	bin, err := m.lookupBinary()
	if err != nil {
		return false, "", ""
	}
	return true, bin, ""
}

func (m *TunnelManager) supervise(ctx context.Context, row TunnelRow, bin string, st *runtimeState, isRestart bool) {
	defer close(st.done)
	backoff := 2 * time.Second
	for {
		if !m.wantRunning(row.ID) {
			m.setStatus(row.ID, "stopped")
			return
		}
		started := m.cfg.Now()
		errRun := m.runOnce(ctx, &row, bin, st)
		if ctx.Err() != nil || !m.wantRunning(row.ID) {
			m.setStatus(row.ID, "stopped")
			return
		}
		uptime := m.cfg.Now().Sub(started)
		if uptime >= uptimeResetAfter {
			backoff = 2 * time.Second
		}
		m.mu.Lock()
		st.restarts++
		st.status = "failed"
		m.mu.Unlock()
		msg := "exited"
		if errRun != nil {
			msg = errRun.Error()
		}
		_ = m.store.SetTunnelRuntime(row.ID, "running", 0, row.PublicURL, msg)
		row.LastError = truncateErr(msg)
		m.cfg.Sleep(backoff)
		if backoff < backoffCap {
			backoff *= 2
			if backoff > backoffCap {
				backoff = backoffCap
			}
		}
		_ = isRestart
		if ctx.Err() != nil || !m.wantRunning(row.ID) {
			m.setStatus(row.ID, "stopped")
			return
		}
	}
}

func (m *TunnelManager) wantRunning(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return false
	}
	st := m.live[id]
	if st == nil || st.stopping {
		return false
	}
	return true
}

func (m *TunnelManager) setStatus(id, status string) {
	m.mu.Lock()
	if st := m.live[id]; st != nil {
		st.status = status
	}
	m.mu.Unlock()
}

func (m *TunnelManager) runOnce(ctx context.Context, row *TunnelRow, bin string, st *runtimeState) error {
	home := filepath.Join(m.cfg.DataDir, "tunnels", row.ID)
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	args := fixedArgs(row)
	env := scrubEnv(row, home)
	cmd := m.cfg.Command(ctx, bin, args, env, home)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	m.mu.Lock()
	st.cmd = cmd
	st.pid = cmd.Process.Pid
	st.adopted = false
	st.status = "running"
	st.startedAt = m.cfg.Now()
	m.mu.Unlock()
	_ = m.store.SetTunnelRuntime(row.ID, "running", cmd.Process.Pid, row.PublicURL, "")
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); m.consume(row, st, stdout) }()
	go func() { defer wg.Done(); m.consume(row, st, stderr) }()
	waitErr := cmd.Wait()
	wg.Wait()
	return waitErr
}

func fixedArgs(row *TunnelRow) []string {
	args := []string{"tunnel", "--no-autoupdate", "--metrics", "127.0.0.1:0"}
	if row.Kind == "quick" {
		args = append(args, "--url", row.TargetURL)
		return args
	}
	args = append(args, "run")
	return args
}

func scrubEnv(row *TunnelRow, home string) []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"KEYSHOP_TUNNEL_ID=" + row.ID,
		"HOME=" + home,
	}
	if row.Kind == "token" && row.Token != "" {
		env = append(env, "TUNNEL_TOKEN="+row.Token)
	}
	return env
}

func (m *TunnelManager) consume(row *TunnelRow, st *runtimeState, r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		redacted := redactLine(line, row.Token)
		if row.Kind == "quick" {
			if match := tryCloudflareURL.FindString(line); match != "" {
				row.PublicURL = match
				_ = m.store.SetTunnelRuntime(row.ID, "running", currentPID(st), match, "")
			}
		}
		m.mu.Lock()
		st.lines = append(st.lines, redacted)
		if len(st.lines) > ringCap {
			st.lines = st.lines[len(st.lines)-ringCap:]
		}
		m.mu.Unlock()
	}
}

func currentPID(st *runtimeState) int {
	if st != nil && st.cmd != nil && st.cmd.Process != nil {
		return st.cmd.Process.Pid
	}
	return 0
}

func redactLine(line, token string) string {
	if token != "" && strings.Contains(line, token) {
		return "[redacted]"
	}
	return line
}

// Stop sets desired=stopped and signals only this manager's child.
func (m *TunnelManager) Stop(id string) error {
	row, err := m.store.GetTunnel(id)
	if err != nil {
		return err
	}
	publicURL := row.PublicURL
	if row.Kind == "quick" {
		// A quick tunnel URL dies with its process, so do not show a stale one.
		publicURL = ""
	}
	if err := m.store.SetTunnelRuntime(id, "stopped", 0, publicURL, row.LastError); err != nil {
		return err
	}
	m.stopChild(id, true)
	return nil
}

func (m *TunnelManager) stopChild(id string, clearLive bool) {
	m.mu.Lock()
	st := m.live[id]
	if st == nil {
		m.mu.Unlock()
		return
	}
	st.stopping = true
	cmd := st.cmd
	pid := st.pid
	adopted := st.adopted
	cancel := st.cancel
	done := st.done
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	proc := (*os.Process)(nil)
	if cmd != nil && cmd.Process != nil {
		proc = cmd.Process
	} else if adopted && pid > 0 {
		proc = &os.Process{Pid: pid}
	}
	if proc != nil {
		_ = m.signalOwned(id, proc, syscall.SIGTERM, adopted)
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(m.cfg.Grace):
			if proc != nil {
				_ = m.signalOwned(id, proc, syscall.SIGKILL, adopted)
			}
			if done != nil {
				select {
				case <-done:
				case <-time.After(m.cfg.Grace):
				}
			}
		}
	}
	m.mu.Lock()
	if clearLive {
		if cur := m.live[id]; cur != nil {
			cur.status = "stopped"
			cur.cmd = nil
		}
	}
	m.mu.Unlock()
}

// Delete stops the child, removes its HOME directory, then removes the row.
func (m *TunnelManager) Delete(id string) error {
	row, err := m.store.GetTunnel(id)
	if err != nil {
		return err
	}
	m.stopChild(id, true)
	home := filepath.Join(m.cfg.DataDir, "tunnels", id)
	if err := removeTunnelHome(m.cfg.DataDir, home, id); err != nil {
		return err
	}
	_ = row
	m.mu.Lock()
	delete(m.live, id)
	m.mu.Unlock()
	return m.store.DeleteTunnel(id)
}

func removeTunnelHome(dataDir, home, id string) error {
	root := filepath.Clean(filepath.Join(dataDir, "tunnels"))
	clean := filepath.Clean(home)
	if id == "" || id == "." || strings.Contains(id, "/") || strings.Contains(id, "..") {
		return fmt.Errorf("refuse to remove tunnel home")
	}
	if clean != filepath.Join(root, id) {
		return fmt.Errorf("refuse to remove tunnel home")
	}
	if _, err := os.Stat(clean); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return os.RemoveAll(clean)
}

// Restart stops then starts. Used when an update changes a running tunnel.
func (m *TunnelManager) Restart(id string) error {
	if err := m.Stop(id); err != nil {
		return err
	}
	return m.Start(id)
}

func validateName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > nameMax {
		return "", fmt.Errorf("%w: name", ErrTunnelInvalid)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("%w: name", ErrTunnelInvalid)
		}
	}
	return name, nil
}

func validateKind(kind string) error {
	if kind != "token" && kind != "quick" {
		return fmt.Errorf("%w: kind", ErrTunnelInvalid)
	}
	return nil
}

func validateTarget(raw string, required bool) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		if required {
			return "", fmt.Errorf("%w: target_url", ErrTunnelInvalid)
		}
		return "", nil
	}
	if strings.ContainsAny(raw, " \t\r\n") {
		return "", fmt.Errorf("%w: target_url", ErrTunnelInvalid)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return "", fmt.Errorf("%w: target_url", ErrTunnelInvalid)
	}
	return raw, nil
}

func validateToken(token string, required bool) error {
	if token == "" {
		if required {
			return fmt.Errorf("%w: token", ErrTunnelInvalid)
		}
		return nil
	}
	if len(token) < tokenMin || len(token) > tokenMax || strings.ContainsAny(token, " \t\r\n") {
		return fmt.Errorf("%w: token", ErrTunnelInvalid)
	}
	return nil
}
