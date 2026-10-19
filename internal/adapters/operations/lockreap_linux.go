//go:build linux

package operations

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/cliversion"
	"github.com/ctxloom/ctxloom/internal/shared/procpin"
)

// listProcesses reads every running process out of /proc. A process gone
// before it could be read, and a zombie — which runs nothing and holds no
// lock — are left out; one whose binary cannot be read keeps the reason
// (RunningProcess.ExeErr), since that is not the same as having none. A
// process this account cannot even name (another account's, under a /proc
// mounted hidepid) is listed without a name, and so is judged by none.
func listProcesses() ([]RunningProcess, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, fmt.Errorf("read /proc: %w", err)
	}
	var procs []RunningProcess
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		st, err := procpin.ReadStat(pid)
		if errors.Is(err, fs.ErrNotExist) || (err == nil && st.State == 'Z') {
			continue
		}
		dir := "/proc/" + e.Name()
		comm, cerr := os.ReadFile(dir + "/comm")
		if errors.Is(cerr, fs.ErrNotExist) {
			continue
		}
		p := RunningProcess{PID: pid, Comm: strings.TrimSuffix(string(comm), "\n")}
		p.Exe, p.ExeErr = os.Readlink(dir + "/exe")
		if p.ExeErr != nil && procGone(pid) {
			continue
		}
		procs = append(procs, p)
	}
	return procs, nil
}

// processVersion asks p's own binary — through /proc/<pid>/exe, the kernel's
// link to the very file the process runs — for its version payload.
func processVersion(p RunningProcess) ([]byte, error) {
	raw, err := cliversion.Output("/proc/" + strconv.Itoa(p.PID) + "/exe")
	if err != nil && procGone(p.PID) {
		return nil, ErrProcessGone
	}
	return raw, err
}

// procGone reports whether pid has left /proc.
func procGone(pid int) bool {
	_, err := os.Stat("/proc/" + strconv.Itoa(pid))
	return errors.Is(err, fs.ErrNotExist)
}
