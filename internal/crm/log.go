package crm

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// Log is the record of everything that has been done: one event a line,
// appended and flushed to the disk before the change it describes is
// acknowledged, each line carrying a checksum of itself and of every line
// before it.
//
// That chain is what makes the file trustworthy as an audit trail and safe as
// storage. A line edited, removed or put in by hand breaks the chain from
// there on, and Open says where. A line half written when the power went —
// the only damage an append can do — is the last one, fails its checksum, and
// is cut off, which loses exactly the change that was never acknowledged.
type Log struct {
	path string

	mu    sync.Mutex
	f     *os.File
	seq   int64
	chain string
	now   func() time.Time
}

// events.jsonl holds personal data: the directory is the owner's alone.
const logFile = "events.jsonl"

// OpenLog opens the log in dir, creating it if there is none, checks every
// line of it, and hands each event to replay in order. It reports how many
// bytes of a torn last line it cut off.
func OpenLog(dir string, replay func(Event) error) (*Log, int, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, 0, fmt.Errorf("crm: %w", err)
	}
	path := filepath.Join(dir, logFile)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, 0, fmt.Errorf("crm: %w", err)
	}
	// One writer: a second process appending would number its events from
	// the same place and break the chain for both.
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, 0, fmt.Errorf("crm: %s is in use by another process", path)
	}
	l := &Log{path: path, f: f}
	good, torn, err := l.read(replay)
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	if torn > 0 {
		if err := f.Truncate(good); err != nil {
			f.Close()
			return nil, 0, fmt.Errorf("crm: cutting a half-written line off the log: %w", err)
		}
	}
	if _, err := f.Seek(good, 0); err != nil {
		f.Close()
		return nil, 0, fmt.Errorf("crm: %w", err)
	}
	return l, torn, nil
}

// read checks the whole log. good is the length of the part that holds; torn
// the bytes after it, which are only ever a last line that was not finished.
func (l *Log) read(replay func(Event) error) (good int64, torn int, err error) {
	if _, err := l.f.Seek(0, 0); err != nil {
		return 0, 0, fmt.Errorf("crm: %w", err)
	}
	info, err := l.f.Stat()
	if err != nil {
		return 0, 0, fmt.Errorf("crm: %w", err)
	}
	r := bufio.NewReaderSize(l.f, 1<<20)
	for n := 1; ; n++ {
		line, rerr := r.ReadBytes('\n')
		if len(line) == 0 && rerr != nil {
			break
		}
		last := good+int64(len(line)) >= info.Size()
		ev, sum, perr := checkLine(line, l.chain)
		if perr != nil || rerr != nil || ev.Seq != l.seq+1 {
			// Only a last line that never got its newline is a torn write.
			// Anything else is a log that has been changed.
			if last && rerr != nil {
				return good, len(line), nil
			}
			if perr == nil {
				perr = fmt.Errorf("it is numbered %d and should be %d", ev.Seq, l.seq+1)
			}
			return 0, 0, fmt.Errorf("crm: %s line %d does not follow from the lines before it (%v): the log has been changed or damaged; restore it from a backup", l.path, n, perr)
		}
		if replay != nil {
			if err := replay(ev); err != nil {
				return 0, 0, fmt.Errorf("crm: %s line %d: %w", l.path, n, err)
			}
		}
		l.seq, l.chain = ev.Seq, sum
		good += int64(len(line))
	}
	return good, 0, nil
}

// checkLine parses one line and confirms its checksum follows from prev.
func checkLine(line []byte, prev string) (Event, string, error) {
	var ev Event
	if len(line) == 0 || line[len(line)-1] != '\n' {
		return ev, "", errors.New("it is not a whole line")
	}
	if err := json.Unmarshal(line, &ev); err != nil {
		return ev, "", errors.New("it is not an event")
	}
	claimed := ev.Chain
	ev.Chain = ""
	body, err := json.Marshal(ev)
	if err != nil {
		return ev, "", err
	}
	if sum := chainSum(prev, body); sum != claimed {
		return ev, "", errors.New("its checksum is wrong")
	}
	ev.Chain = claimed
	return ev, claimed, nil
}

func chainSum(prev string, body []byte) string {
	h := sha256.New()
	h.Write([]byte(prev))
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))[:24]
}

// Append numbers, stamps, checksums and writes an event, and returns it as
// written once it is on the disk.
func (l *Log) Append(ev Event) (Event, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return ev, errors.New("crm: the log is closed")
	}
	ev.Seq = l.seq + 1
	if ev.At.IsZero() {
		if l.now != nil {
			ev.At = l.now()
		} else {
			ev.At = time.Now()
		}
	}
	ev.At = ev.At.Round(time.Millisecond)
	ev.Chain = ""
	body, err := json.Marshal(ev)
	if err != nil {
		return ev, fmt.Errorf("crm: %w", err)
	}
	// What is checked on reading is the event as it reads back, so that is
	// what the checksum is of.
	var back Event
	if err := json.Unmarshal(body, &back); err != nil {
		return ev, fmt.Errorf("crm: %w", err)
	}
	if body, err = json.Marshal(back); err != nil {
		return ev, fmt.Errorf("crm: %w", err)
	}
	ev = back
	ev.Chain = chainSum(l.chain, body)
	line, err := json.Marshal(ev)
	if err != nil {
		return ev, fmt.Errorf("crm: %w", err)
	}
	if _, err := l.f.Write(append(line, '\n')); err != nil {
		return ev, fmt.Errorf("crm: writing the log: %w", err)
	}
	if err := l.f.Sync(); err != nil {
		return ev, fmt.Errorf("crm: writing the log: %w", err)
	}
	l.seq, l.chain = ev.Seq, ev.Chain
	return ev, nil
}

// Seq is the number of the last event written.
func (l *Log) Seq() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.seq
}

// Close releases the file.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

// VerifyLog reads a log without opening it for writing and reports how many
// events it holds, or what is wrong with it.
func VerifyLog(dir string) (int64, error) {
	f, err := os.Open(filepath.Join(dir, logFile))
	if err != nil {
		return 0, fmt.Errorf("crm: %w", err)
	}
	defer f.Close()
	l := &Log{path: f.Name(), f: f}
	_, torn, err := l.read(nil)
	if err != nil {
		return l.seq, err
	}
	if torn > 0 {
		return l.seq, fmt.Errorf("crm: %s ends in %d bytes of a line that was never finished; they are cut off when the service next starts", l.path, torn)
	}
	return l.seq, nil
}
