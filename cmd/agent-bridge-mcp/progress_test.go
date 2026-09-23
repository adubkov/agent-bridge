package main

import (
	"context"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// fakeProgressSession is a server.ClientSession that captures notifications on
// a buffered channel, so tests can observe what the progress reporter sends.
type fakeProgressSession struct {
	ch chan mcp.JSONRPCNotification
}

func (f *fakeProgressSession) Initialize() {}
func (f *fakeProgressSession) Initialized() bool {
	return true
}
func (f *fakeProgressSession) NotificationChannel() chan<- mcp.JSONRPCNotification {
	return f.ch
}
func (f *fakeProgressSession) SessionID() string { return "fake" }

var _ server.ClientSession = (*fakeProgressSession)(nil)

// drainProgress collects all pending notifications and returns their params,
// failing the test on a wrong method or missing progress fields.
func drainProgress(t *testing.T, ch chan mcp.JSONRPCNotification) []mcp.NotificationParams {
	t.Helper()
	var out []mcp.NotificationParams
	for {
		select {
		case n := <-ch:
			if n.Notification.Method != string(mcp.MethodNotificationProgress) {
				t.Fatalf("notification method = %q; want %q", n.Notification.Method, mcp.MethodNotificationProgress)
			}
			p := n.Notification.Params
			if p.AdditionalFields["progressToken"] == nil || p.AdditionalFields["progress"] == nil {
				t.Fatalf("params missing progress fields: %v", p.AdditionalFields)
			}
			out = append(out, p)
		default:
			return out
		}
	}
}

func progressFields(p mcp.NotificationParams) (token string, value float64, message string) {
	token, _ = p.AdditionalFields["progressToken"].(string)
	value, _ = p.AdditionalFields["progress"].(float64)
	message, _ = p.AdditionalFields["message"].(string)
	return
}

func TestProgressIntervalDefault(t *testing.T) {
	t.Setenv(progressIntervalEnv, "")
	if got := progressInterval(); got != defaultProgressIntervalMS*time.Millisecond {
		t.Errorf("progressInterval() with unset env = %v; want %v", got, defaultProgressIntervalMS*time.Millisecond)
	}
}

func TestProgressIntervalOverride(t *testing.T) {
	cases := []struct {
		env  string
		want time.Duration
	}{
		{"100", 100 * time.Millisecond},
		{"0", 0},
		{"-5", 0},
		{"garbage", defaultProgressIntervalMS * time.Millisecond},
	}
	for _, c := range cases {
		t.Setenv(progressIntervalEnv, c.env)
		if got := progressInterval(); got != c.want {
			t.Errorf("progressInterval() with %s=%q = %v; want %v", progressIntervalEnv, c.env, got, c.want)
		}
	}
}

func TestProgressReporterNoTokenNoop(t *testing.T) {
	sess := &fakeProgressSession{ch: make(chan mcp.JSONRPCNotification, 8)}
	stop := startProgressReporter(context.Background(), sess, claudeBackend, runOpts{task: "x"})
	time.Sleep(20 * time.Millisecond)
	stop()
	if n := len(drainProgress(t, sess.ch)); n != 0 {
		t.Fatalf("no progress token: got %d notifications; want none", n)
	}
}

func TestProgressReporterNilSessionNoop(t *testing.T) {
	stop := startProgressReporter(context.Background(), nil, claudeBackend, runOpts{progressToken: "t"})
	time.Sleep(20 * time.Millisecond)
	stop()
}

func TestProgressReporterDisabledByEnv(t *testing.T) {
	t.Setenv(progressIntervalEnv, "0")
	sess := &fakeProgressSession{ch: make(chan mcp.JSONRPCNotification, 8)}
	stop := startProgressReporter(context.Background(), sess, claudeBackend, runOpts{progressToken: "t"})
	time.Sleep(20 * time.Millisecond)
	stop()
	if n := len(drainProgress(t, sess.ch)); n != 0 {
		t.Fatalf("interval disabled: got %d notifications; want none", n)
	}
}

func TestProgressReporterEmitsMonotonic(t *testing.T) {
	t.Setenv(progressIntervalEnv, "30")
	sess := &fakeProgressSession{ch: make(chan mcp.JSONRPCNotification, 64)}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	stop := startProgressReporter(ctx, sess, claudeBackend, runOpts{progressToken: "pt-42"})
	time.Sleep(150 * time.Millisecond)
	stop()

	got := drainProgress(t, sess.ch)
	if len(got) < 2 {
		t.Fatalf("got %d progress notifications in 150ms at 30ms interval; want >=2", len(got))
	}
	var last float64
	for i, p := range got {
		token, value, message := progressFields(p)
		if token != "pt-42" {
			t.Errorf("notification %d: progressToken = %q; want pt-42", i, token)
		}
		if value <= last {
			t.Errorf("notification %d: progress = %v; not increasing (last %v)", i, value, last)
		}
		last = value
		if message == "" {
			t.Errorf("notification %d: empty message", i)
		}
	}
}

func TestProgressReporterSharedCounter(t *testing.T) {
	t.Setenv(progressIntervalEnv, "30")
	var counter atomic.Int64
	s1 := &fakeProgressSession{ch: make(chan mcp.JSONRPCNotification, 64)}
	s2 := &fakeProgressSession{ch: make(chan mcp.JSONRPCNotification, 64)}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	o := runOpts{progressToken: "tok", progressCounter: &counter}
	stop1 := startProgressReporter(ctx, s1, claudeBackend, o)
	stop2 := startProgressReporter(ctx, s2, codexBackend, o)
	time.Sleep(150 * time.Millisecond)
	stop1()
	stop2()

	var vals []float64
	for _, p := range append(drainProgress(t, s1.ch), drainProgress(t, s2.ch)...) {
		_, v, _ := progressFields(p)
		vals = append(vals, v)
	}
	if len(vals) < 2 {
		t.Fatalf("got %d combined notifications; want >=2", len(vals))
	}
	sort.Float64s(vals)
	for i := 1; i < len(vals); i++ {
		if vals[i] <= vals[i-1] {
			t.Fatalf("combined stream not strictly increasing: %v at %d (values %v)", vals[i], i, vals)
		}
	}
}

func TestProgressReporterStopsOnContextDone(t *testing.T) {
	t.Setenv(progressIntervalEnv, "20")
	sess := &fakeProgressSession{ch: make(chan mcp.JSONRPCNotification, 64)}
	ctx, cancel := context.WithCancel(context.Background())

	stop := startProgressReporter(ctx, sess, claudeBackend, runOpts{progressToken: "t"})
	time.Sleep(60 * time.Millisecond)
	cancel()
	before := len(drainProgress(t, sess.ch))
	if before == 0 {
		t.Fatalf("no notifications before cancel")
	}
	time.Sleep(60 * time.Millisecond)
	after := len(drainProgress(t, sess.ch))
	if after != 0 {
		t.Fatalf("got %d notifications after context cancel; want none", after)
	}
	stop()
}
