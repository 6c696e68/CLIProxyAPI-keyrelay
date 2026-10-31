package keyshop

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
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
	// HostBaseURL is the base URL the host told the plugin it listens on. It is
	// the fallback target when the plugin config sets none.
	HostBaseURL string
	Command     commandFactory
	Now         func() time.Time
	// ProcRoot overrides /proc for ownership checks. Tests point it at a temp dir.
	ProcRoot string
	// Grace overrides the SIGTERM wait. Tests use a short value.
	Grace time.Duration
	// Sleep overrides restart backoff. Tests return immediately. Nil uses an
	// interruptible timer, so Shutdown never waits out a backoff.
	Sleep func(time.Duration)
	// KeepAlive leaves children running across Shutdown. Nil means true. The
	// effective value also needs ownership checks, so non-Linux hosts still stop
	// their children.
	KeepAlive *bool
	// WatchInterval is how often a child and its log file are checked. Zero or
	// negative uses DefaultTunnelWatchInterval.
	WatchInterval time.Duration
	// LogMaxKB caps the child log in KiB. Zero or negative uses
	// DefaultTunnelLogMaxKB.
	LogMaxKB int64
	// SystemdRun is the systemd-run path used to detach a kept-alive child into
	// a transient scope. Empty never uses systemd.
	SystemdRun string
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
	id        string
	status    string
	restarts  int
	startedAt time.Time
	cmd       *exec.Cmd
	pid       int
	cancel    context.CancelFunc
	done      chan struct{}
	stopWatch chan struct{}
	stopping  bool
	adopted   bool
	// urlCaptured is true once the current child's quick URL is stored, so the
	// watcher stops reading the log on every tick.
	urlCaptured bool
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
	if cfg.WatchInterval <= 0 {
		cfg.WatchInterval = DefaultTunnelWatchInterval
	}
	if cfg.LogMaxKB <= 0 {
		cfg.LogMaxKB = DefaultTunnelLogMaxKB
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

// SystemdRunPath returns the systemd-run binary when this host can host a
// transient scope: Linux, a running systemd, root, and systemd-run on PATH.
// Tests never call it.
func SystemdRunPath() string {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		return ""
	}
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return ""
	}
	path, err := exec.LookPath("systemd-run")
	if err != nil {
		return ""
	}
	return path
}

// KeepAliveEnabled reports the configured keep-alive flag, before the platform
// decides whether adoption is possible.
func (m *TunnelManager) KeepAliveEnabled() bool {
	if m == nil {
		return false
	}
	return m.cfg.KeepAlive == nil || *m.cfg.KeepAlive
}

// WatchInterval returns the effective interval between ownership checks.
func (m *TunnelManager) WatchInterval() time.Duration {
	if m == nil {
		return DefaultTunnelWatchInterval
	}
	return m.cfg.WatchInterval
}

// LogMaxKB returns the effective child log cap in KiB.
func (m *TunnelManager) LogMaxKB() int64 {
	if m == nil {
		return DefaultTunnelLogMaxKB
	}
	return m.cfg.LogMaxKB
}

// keepAlive reports whether Shutdown may leave children running. Detaching only
// works where a recorded pid can be checked, so non-Linux hosts keep stopping
// their children and never create a duplicate after a reload.
func (m *TunnelManager) keepAlive() bool {
	if m == nil {
		return false
	}
	return m.KeepAliveEnabled() && ownershipPossible(m.cfg.ProcRoot)
}

// ownershipPossible is the rule ownsPID uses: Linux, or an absolute non-/proc
// root that a test controls.
func ownershipPossible(procRoot string) bool {
	if runtime.GOOS == "linux" {
		return true
	}
	return procRoot != "" && procRoot != "/proc" && filepath.IsAbs(procRoot)
}

func (m *TunnelManager) isClosed() bool {
	if m == nil {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
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

// Reconcile starts rows that should be running after the ownership check. An
// enabled manager adopts a live owned pid and watches it. A disabled manager
// never spawns and stops every owned pid its rows still record.
func (m *TunnelManager) Reconcile() {
	if m == nil || m.store == nil || m.isClosed() {
		return
	}
	rows, err := m.store.ListTunnels()
	if err != nil {
		return
	}
	if !m.Enabled() {
		for _, row := range rows {
			m.stopDisabled(row)
		}
		return
	}
	for _, row := range rows {
		if m.adoptOrClear(row) {
			continue
		}
		if row.Desired == "running" || row.Autostart {
			_ = m.Start(row.ID)
		}
	}
}

// stopDisabled stops an owned pid and clears it. Desired is kept so a later
// Reconcile with tunnels enabled starts the tunnel again.
func (m *TunnelManager) stopDisabled(row TunnelRow) {
	if row.PID <= 0 {
		return
	}
	if ownsPID(m.cfg.ProcRoot, row.PID, row.ID) {
		m.stopRecorded(row.ID, row.PID)
	}
	_ = m.store.SetTunnelRuntime(row.ID, row.Desired, 0, row.PublicURL, row.LastError)
}

// adoptOrClear tracks a pid this plugin still owns and watches it. Anything else
// is cleared and never signaled. owned is true only when the pid is alive and
// owned, so the caller must not start a second child.
func (m *TunnelManager) adoptOrClear(row TunnelRow) bool {
	if row.PID <= 0 {
		return false
	}
	if !ownsPID(m.cfg.ProcRoot, row.PID, row.ID) {
		_ = m.store.SetTunnelRuntime(row.ID, row.Desired, 0, row.PublicURL, row.LastError)
		return false
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return true
	}
	if st := m.live[row.ID]; st != nil && st.pid == row.PID && !st.stopping && !stateExited(st) {
		m.mu.Unlock()
		return true
	}
	ctx, cancel := context.WithCancel(context.Background())
	st := &runtimeState{
		id: row.ID, status: "running", pid: row.PID, adopted: true,
		urlCaptured: row.Kind == "quick" && row.PublicURL != "",
		startedAt:   m.cfg.Now(), cancel: cancel,
		done: make(chan struct{}), stopWatch: make(chan struct{}),
	}
	m.live[row.ID] = st
	m.mu.Unlock()
	go m.supervise(ctx, row.ID, st)
	return true
}

func ownsPID(procRoot string, pid int, id string) bool {
	// On non-Linux hosts a recorded pid is never owned, so nothing is signaled.
	// Tests may pass an absolute fake proc root that is not the real /proc.
	if !ownershipPossible(procRoot) {
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

// stateExited reports whether the supervisor for st has returned.
func stateExited(st *runtimeState) bool {
	if st == nil || st.done == nil {
		return true
	}
	select {
	case <-st.done:
		return true
	default:
		return false
	}
}

// Shutdown stops watching every child. With keep-alive, the children and their
// rows are left alone so the next manager can adopt them. Otherwise every child
// is signaled, as before.
func (m *TunnelManager) Shutdown() {
	if m == nil {
		return
	}
	keepAlive := m.keepAlive()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	states := make([]*runtimeState, 0, len(m.live))
	for _, st := range m.live {
		if st != nil {
			states = append(states, st)
		}
	}
	m.mu.Unlock()
	if keepAlive {
		for _, st := range states {
			if st.stopWatch != nil {
				close(st.stopWatch)
			}
		}
		// Wait so a spawn in flight records its pid before this manager is gone.
		for _, st := range states {
			if st.done != nil {
				<-st.done
			}
		}
		return
	}
	var wg sync.WaitGroup
	for _, st := range states {
		wg.Add(1)
		go func(st *runtimeState) {
			defer wg.Done()
			m.stopChild(st.id, false, 0)
		}(st)
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

// Logs returns the last lines of the child log with the token redacted. The log
// file outlives the manager, so this also works for an adopted child and after
// a plugin reload.
func (m *TunnelManager) Logs(id string, lines int) []string {
	if lines <= 0 {
		lines = 200
	}
	if m == nil {
		return []string{}
	}
	path, ok := tunnelLogPath(m.cfg.DataDir, id)
	if !ok {
		return []string{}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return []string{}
	}
	token := ""
	if m.store != nil {
		if row, errRow := m.store.GetTunnel(id); errRow == nil {
			token = row.Token
		}
	}
	text := strings.TrimRight(string(raw), "\n")
	if text == "" {
		return []string{}
	}
	all := strings.Split(text, "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	out := make([]string, len(all))
	for i, line := range all {
		out[i] = redactLine(line, token)
	}
	return out
}

// Start runs the child for id. A recorded pid that is alive and owned is adopted
// instead of started, so a reload never creates a second child.
func (m *TunnelManager) Start(id string) error {
	if !m.Enabled() {
		return errTunnelsDisabled
	}
	row, err := m.store.GetTunnel(id)
	if err != nil {
		return err
	}
	adopted := row.PID > 0 && ownsPID(m.cfg.ProcRoot, row.PID, id)
	if !adopted {
		if _, errLookup := m.lookupBinary(); errLookup != nil {
			return errLookup
		}
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errManagerClosed
	}
	if st := m.live[id]; st != nil && !st.stopping && !stateExited(st) {
		m.mu.Unlock()
		return ErrAlreadyRunning
	}
	pid := 0
	if adopted {
		pid = row.PID
	}
	// Write the runtime row before the state is registered. A failed write must
	// leave no state behind: a keep-alive Shutdown waits on every registered
	// state's done channel, and that channel is only closed by a supervisor.
	if err := m.store.SetTunnelRuntime(id, "running", pid, row.PublicURL, ""); err != nil {
		m.mu.Unlock()
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	st := &runtimeState{
		id: id, status: "starting", cancel: cancel,
		done: make(chan struct{}), stopWatch: make(chan struct{}),
	}
	if prev := m.live[id]; prev != nil {
		st.restarts = prev.restarts
	}
	if adopted {
		st.pid = row.PID
		st.adopted = true
		st.status = "running"
		st.urlCaptured = row.Kind == "quick" && row.PublicURL != ""
		st.startedAt = m.cfg.Now()
	}
	m.live[id] = st
	m.mu.Unlock()
	go m.supervise(ctx, id, st)
	return nil
}

var (
	ErrAlreadyRunning     = errors.New("already running")
	ErrCloudflaredMissing = errors.New("cloudflared not found")
	errTunnelsDisabled    = errors.New("tunnels disabled")
	errManagerClosed      = errors.New("tunnel manager stopped")
	errDetached           = errors.New("tunnel watch detached")
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

// supervise owns one tunnel until it is stopped. It re-reads the row before
// every spawn, so a restart never creates a duplicate child for a pid that is
// still alive and owned.
func (m *TunnelManager) supervise(ctx context.Context, id string, st *runtimeState) {
	defer close(st.done)
	backoff := 2 * time.Second
	for {
		if !m.wanted(st) {
			m.setStatus(st, "stopped")
			return
		}
		row, err := m.store.GetTunnel(id)
		if err != nil {
			m.setStatus(st, "stopped")
			return
		}
		started := m.cfg.Now()
		var errRun error
		switch {
		case m.adoptedPID(st) > 0:
			// Reconcile or Start already checked this pid. Watching it here
			// first keeps a late-starting supervisor from spawning a duplicate
			// when the row changes right after adoption.
			errRun = m.waitAdopted(ctx, st, &row)
		case row.PID > 0 && ownsPID(m.cfg.ProcRoot, row.PID, id):
			m.beginAdopted(st, &row)
			errRun = m.waitAdopted(ctx, st, &row)
		default:
			bin, errBin := m.lookupBinary()
			if errBin != nil {
				errRun = errBin
			} else {
				errRun = m.runOnce(ctx, &row, bin, st)
			}
		}
		if ctx.Err() != nil || !m.wanted(st) {
			m.setStatus(st, "stopped")
			return
		}
		if m.cfg.Now().Sub(started) >= uptimeResetAfter {
			backoff = 2 * time.Second
		}
		msg := "exited"
		if errRun != nil {
			msg = errRun.Error()
		}
		m.mu.Lock()
		if m.closed || st.stopping || m.live[id] != st {
			m.mu.Unlock()
			return
		}
		st.restarts++
		st.status = "failed"
		// The next pass must decide from the row again, not from the pid that
		// just went away.
		st.adopted = false
		st.pid = 0
		_ = m.store.SetTunnelRuntime(id, "running", 0, row.PublicURL, msg)
		m.mu.Unlock()
		if !m.pause(ctx, st, backoff) {
			m.setStatus(st, "stopped")
			return
		}
		if backoff < backoffCap {
			backoff *= 2
			if backoff > backoffCap {
				backoff = backoffCap
			}
		}
	}
}

// wanted reports whether st is still the live supervisor, and the manager has
// neither been closed nor told this tunnel to stop.
func (m *TunnelManager) wanted(st *runtimeState) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || st.stopping {
		return false
	}
	return m.live[st.id] == st
}

func (m *TunnelManager) setStatus(st *runtimeState, status string) {
	m.mu.Lock()
	if m.live[st.id] == st {
		st.status = status
	}
	m.mu.Unlock()
}

// adoptedPID returns the pid a state already adopted, or 0 when the supervisor
// must decide from the row.
func (m *TunnelManager) adoptedPID(st *runtimeState) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if st.adopted {
		return st.pid
	}
	return 0
}

func (m *TunnelManager) beginAdopted(st *runtimeState, row *TunnelRow) {
	m.mu.Lock()
	st.cmd = nil
	st.pid = row.PID
	st.adopted = true
	st.status = "running"
	st.urlCaptured = row.Kind == "quick" && row.PublicURL != ""
	if st.startedAt.IsZero() {
		st.startedAt = m.cfg.Now()
	}
	m.mu.Unlock()
}

// pause waits before a supervised restart and reports whether the tunnel is
// still wanted. A test Sleep hook keeps the wait out of the way.
func (m *TunnelManager) pause(ctx context.Context, st *runtimeState, d time.Duration) bool {
	if m.cfg.Sleep != nil {
		m.cfg.Sleep(d)
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	case <-st.stopWatch:
		return false
	}
}

// waitAdopted follows a child this manager did not spawn. The marker re-check
// is the only liveness test, so a reused pid is never treated as the tunnel.
func (m *TunnelManager) waitAdopted(ctx context.Context, st *runtimeState, row *TunnelRow) error {
	ticker := time.NewTicker(m.cfg.WatchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-st.stopWatch:
			return errDetached
		case <-ticker.C:
			m.watchTick(st, row)
			m.mu.Lock()
			pid := st.pid
			m.mu.Unlock()
			if !ownsPID(m.cfg.ProcRoot, pid, row.ID) {
				return errors.New("process exited")
			}
		}
	}
}

func (m *TunnelManager) runOnce(ctx context.Context, row *TunnelRow, bin string, st *runtimeState) error {
	if !m.wanted(st) {
		// A stop that already landed must not build or start anything.
		return errDetached
	}
	home := filepath.Join(m.cfg.DataDir, "tunnels", row.ID)
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	logPath := filepath.Join(home, "cloudflared.log")
	// A file instead of a pipe: a write to a closed pipe would kill cloudflared
	// and the pipe reader dies with the plugin process.
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	env := scrubEnv(row, home)
	// The spawn decision, the command and the pid record share the manager lock,
	// so a stop that lands while a child is starting either sees the recorded
	// child or wins and no child is started.
	m.mu.Lock()
	if m.closed || st.stopping || m.live[st.id] != st {
		m.mu.Unlock()
		_ = logFile.Close()
		return errDetached
	}
	binName, args := m.commandLine(row, bin)
	cmd := m.cfg.Command(ctx, binName, args, env, home)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	detachProcess(cmd)
	if err := cmd.Start(); err != nil {
		m.mu.Unlock()
		_ = logFile.Close()
		return err
	}
	_ = logFile.Close()
	pid := cmd.Process.Pid
	publicURL := row.PublicURL
	if row.Kind == "quick" {
		// A quick URL belongs to one process. Clear the previous child's URL so
		// a reload before the new URL is captured does not mark it as the live
		// child's URL and skip the log.
		publicURL = ""
		row.PublicURL = ""
	}
	st.cmd = cmd
	st.pid = pid
	st.adopted = false
	st.status = "running"
	st.urlCaptured = false
	st.startedAt = m.cfg.Now()
	_ = m.store.SetTunnelRuntime(row.ID, "running", pid, publicURL, "")
	m.mu.Unlock()

	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	ticker := time.NewTicker(m.cfg.WatchInterval)
	defer ticker.Stop()
	for {
		select {
		case errWait := <-waitDone:
			return errWait
		case <-ctx.Done():
			// The stop path sends SIGTERM. Do not return before the process is
			// gone, so a stopped tunnel never leaves a child behind.
			select {
			case <-waitDone:
			case <-time.After(m.cfg.Grace):
				_ = cmd.Process.Kill()
				select {
				case <-waitDone:
				case <-time.After(m.cfg.Grace):
				}
			}
			return ctx.Err()
		case <-st.stopWatch:
			// Keep-alive shutdown: leave the child running for the next manager.
			return errDetached
		case <-ticker.C:
			m.watchTick(st, row)
		}
	}
}

// captureURL reads the trycloudflare URL from the child log for a quick tunnel.
// Only the supervisor goroutine calls it with its own row copy, so no shared row
// state is written from two readers.
// watchTick runs on every watcher tick: it keeps the child log bounded and
// captures a quick-tunnel URL while the current child still needs one.
func (m *TunnelManager) watchTick(st *runtimeState, row *TunnelRow) {
	// Capture before the cap truncates: a URL written in the same burst as the
	// over-cap data would otherwise be dropped unread.
	m.captureURL(st, row)
	m.enforceLogCap(row)
}

// enforceLogCap truncates the child log to 0 when it is over the configured
// cap. The child writes through an O_APPEND fd, so the next write lands at
// offset 0 instead of leaving a sparse hole.
func (m *TunnelManager) enforceLogCap(row *TunnelRow) {
	path, ok := tunnelLogPath(m.cfg.DataDir, row.ID)
	if !ok {
		return
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() <= m.cfg.LogMaxKB*bytesPerKB {
		return
	}
	_ = os.Truncate(path, 0)
}

// captureURL reads the trycloudflare URL from the child log once per child. An
// adopted child whose row already stores a URL never reads the log for it, so a
// kept-alive tunnel does not read the whole file on every tick.
func (m *TunnelManager) captureURL(st *runtimeState, row *TunnelRow) {
	if st == nil || row.Kind != "quick" {
		return
	}
	m.mu.Lock()
	captured := st.urlCaptured
	m.mu.Unlock()
	if captured {
		return
	}
	path, ok := tunnelLogPath(m.cfg.DataDir, row.ID)
	if !ok {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	match := tryCloudflareURL.Find(raw)
	if len(match) == 0 {
		return
	}
	publicURL := string(match)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || st.stopping || m.live[st.id] != st || st.urlCaptured {
		return
	}
	if err := m.store.SetTunnelRuntime(row.ID, "running", st.pid, publicURL, ""); err != nil {
		return
	}
	row.PublicURL = publicURL
	st.urlCaptured = true
}

// commandLine builds the argv for one child. A kept-alive child runs in a
// transient systemd scope, so stopping the host service does not kill it with
// the service control group.
func (m *TunnelManager) commandLine(row *TunnelRow, bin string) (string, []string) {
	args := fixedArgs(row)
	run := strings.TrimSpace(m.cfg.SystemdRun)
	if run == "" || !m.keepAlive() {
		return bin, args
	}
	scope := append([]string{
		"--scope", "--quiet", "--collect", "--unit=" + scopeUnitName(row.ID), "--", bin,
	}, args...)
	return run, scope
}

// scopeUnitName is a unique transient unit name; the tunnel id keeps it
// attributable in systemctl output.
func scopeUnitName(id string) string {
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "keyshop-tunnel-" + id + "-00000000"
	}
	return "keyshop-tunnel-" + id + "-" + hex.EncodeToString(buf[:])
}

// tunnelLogPath returns the child log path and refuses an id that would escape
// the tunnels directory.
func tunnelLogPath(dataDir, id string) (string, bool) {
	if dataDir == "" || id == "" || id == "." || strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return "", false
	}
	root := filepath.Clean(filepath.Join(dataDir, "tunnels"))
	path := filepath.Clean(filepath.Join(root, id, "cloudflared.log"))
	if filepath.Dir(filepath.Dir(path)) != root {
		return "", false
	}
	return path, true
}

// DefaultTarget returns the target a tunnel uses when the request does not name
// one: the plugin YAML target first, then the host base URL the host announced.
func (m *TunnelManager) DefaultTarget() string {
	if m == nil {
		return ""
	}
	if target := strings.TrimSpace(m.cfg.TargetURL); target != "" {
		return target
	}
	return strings.TrimSpace(m.cfg.HostBaseURL)
}

func fixedArgs(row *TunnelRow) []string {
	args := []string{"tunnel", "--no-autoupdate", "--metrics", "127.0.0.1:0"}
	if row.Kind == "quick" {
		args = append(args, "--url", row.TargetURL)
		if httpsLoopbackTarget(row.TargetURL) {
			// The proxy may serve a self-signed certificate, which cloudflared
			// would reject with a 502 on every request.
			args = append(args, "--no-tls-verify")
		}
		return args
	}
	args = append(args, "run")
	return args
}

// httpsLoopbackTarget reports whether target is HTTPS on a loopback host.
func httpsLoopbackTarget(target string) bool {
	u, err := url.Parse(strings.TrimSpace(target))
	if err != nil || u.Scheme != "https" {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "127.0.0.1", "::1", "localhost":
		return true
	}
	return false
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

func redactLine(line, token string) string {
	if token != "" && strings.Contains(line, token) {
		return "[redacted]"
	}
	return line
}

// Stop sets desired=stopped and signals only this manager's child, or a
// recorded child this plugin still owns.
func (m *TunnelManager) Stop(id string) error {
	if m == nil || m.store == nil {
		return nil
	}
	if m.isClosed() {
		return errManagerClosed
	}
	row, err := m.store.GetTunnel(id)
	if err != nil {
		return err
	}
	publicURL := row.PublicURL
	if row.Kind == "quick" {
		// A quick tunnel URL dies with its process, so do not show a stale one.
		publicURL = ""
	}
	m.markStopping(id)
	// Capture the recorded pid before the row write clears it: an owned child
	// this manager never adopted still has to be stopped.
	recordedPID := row.PID
	if err := m.store.SetTunnelRuntime(id, "stopped", 0, publicURL, row.LastError); err != nil {
		return err
	}
	m.stopChild(id, true, recordedPID)
	return nil
}

func (m *TunnelManager) markStopping(id string) {
	m.mu.Lock()
	if st := m.live[id]; st != nil {
		st.stopping = true
	}
	m.mu.Unlock()
}

// stopRecorded ends a recorded pid this plugin owns. Ownership is re-checked
// right before each signal, and the pid is polled until it disappears.
func (m *TunnelManager) stopRecorded(id string, pid int) {
	if pid <= 0 || !ownsPID(m.cfg.ProcRoot, pid, id) {
		return
	}
	proc := &os.Process{Pid: pid}
	_ = m.signalOwned(id, proc, syscall.SIGTERM, true)
	if m.waitGone(id, pid) {
		return
	}
	if !ownsPID(m.cfg.ProcRoot, pid, id) {
		return
	}
	_ = m.signalOwned(id, proc, syscall.SIGKILL, true)
	m.waitGone(id, pid)
}

// waitGone polls ownership until pid is gone or Grace runs out.
func (m *TunnelManager) waitGone(id string, pid int) bool {
	step := m.cfg.Grace / 20
	if step <= 0 {
		step = m.cfg.Grace
	}
	deadline := time.Now().Add(m.cfg.Grace)
	for {
		if !ownsPID(m.cfg.ProcRoot, pid, id) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(step)
	}
}

func (m *TunnelManager) stopChild(id string, clearLive bool, recordedPID int) {
	m.mu.Lock()
	st := m.live[id]
	var (
		cmd     *exec.Cmd
		pid     int
		adopted bool
		cancel  context.CancelFunc
		done    chan struct{}
	)
	if st != nil {
		st.stopping = true
		cmd, pid, adopted, cancel, done = st.cmd, st.pid, st.adopted, st.cancel, st.done
	}
	m.mu.Unlock()
	if st == nil {
		// The row can record an owned pid this manager never adopted, for
		// example right after a reload or a quick start/stop.
		m.stopRecorded(id, recordedPID)
		return
	}
	if cancel != nil {
		cancel()
	}
	switch {
	case cmd != nil && cmd.Process != nil:
		// A spawned child is this manager's own process, so no marker check.
		_ = m.signalOwned(id, cmd.Process, syscall.SIGTERM, false)
		if !waitClosed(done, m.cfg.Grace) {
			_ = m.signalOwned(id, cmd.Process, syscall.SIGKILL, false)
			waitClosed(done, m.cfg.Grace)
		}
	case adopted && pid > 0:
		m.stopRecorded(id, pid)
	}
	m.mu.Lock()
	if clearLive {
		if cur := m.live[id]; cur == st {
			cur.status = "stopped"
			cur.cmd = nil
		}
	}
	m.mu.Unlock()
}

func waitClosed(done chan struct{}, d time.Duration) bool {
	if done == nil {
		return true
	}
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// Delete stops the child, removes its HOME directory, then removes the row.
func (m *TunnelManager) Delete(id string) error {
	if m == nil || m.store == nil {
		return nil
	}
	if m.isClosed() {
		return errManagerClosed
	}
	row, err := m.store.GetTunnel(id)
	if err != nil {
		return err
	}
	m.markStopping(id)
	m.stopChild(id, true, row.PID)
	home := filepath.Join(m.cfg.DataDir, "tunnels", id)
	if err := removeTunnelHome(m.cfg.DataDir, home, id); err != nil {
		return err
	}
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
