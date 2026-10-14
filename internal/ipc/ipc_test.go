package ipc

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"
)

func TestHelloStateAndCommandRoundTrip(t *testing.T) {
	hello := Hello{
		PageList:  []int{0, 4, 5, 8},
		Notes:     map[int]string{5: "Größe – “quoted”"},
		StartedAt: time.Now().Add(-time.Minute).Round(time.Millisecond),
	}
	srv, err := Listen(hello)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer srv.Close()

	// State sent before the presenter connects must be replayed on connect.
	wantState := PresenterState{
		Page:           4,
		ListIndex:      1,
		NextPage:       5,
		Paused:         true,
		Fraction:       0.75,
		FractionPerSec: 0.1,
		Overview: PresenterOverviewState{
			Active:        true,
			Phase:         2,
			Anim:          1,
			FromIndex:     2,
			SelectedIndex: 3,
			ExitToIndex:   3,
		},
	}
	srv.Send(wantState)
	go srv.Accept()

	disconnected := make(chan struct{})
	recv, err := Connect(srv.Path(), func() { close(disconnected) })
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		gotHello, ok := recv.Hello()
		gotState, _, _ := recv.Latest()
		if ok && reflect.DeepEqual(gotState, wantState) {
			if !reflect.DeepEqual(gotHello.PageList, hello.PageList) ||
				!reflect.DeepEqual(gotHello.Notes, hello.Notes) ||
				!gotHello.StartedAt.Equal(hello.StartedAt) {
				t.Fatalf("hello = %+v, want %+v", gotHello, hello)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("receiver hello=%v state=%+v, want %+v", ok, gotState, wantState)
		}
		time.Sleep(5 * time.Millisecond)
	}

	wantCmd := PresenterCommand{Name: "digit", Arg: 7}
	recv.SendCommand(wantCmd)
	select {
	case got := <-srv.Commands():
		if got != wantCmd {
			t.Fatalf("command = %+v, want %+v", got, wantCmd)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for command")
	}

	dir := filepath.Dir(srv.Path())
	st, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0o700 {
		t.Fatalf("socket dir perms = %v, want 0700", st.Mode().Perm())
	}

	_ = srv.Close()
	select {
	case <-disconnected:
	case <-time.After(time.Second):
		t.Fatal("receiver did not observe disconnect")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("socket dir should be removed on Close, stat err = %v", err)
	}
}

func TestFractionAtExtrapolatesAndClamps(t *testing.T) {
	st := PresenterState{Fraction: 0.5, FractionPerSec: 0.25}
	recv := time.Now()
	if got := st.FractionAt(recv, recv.Add(time.Second)); got != 0.75 {
		t.Fatalf("FractionAt(+1s) = %v, want 0.75", got)
	}
	if got := st.FractionAt(recv, recv.Add(10*time.Second)); got != 1 {
		t.Fatalf("FractionAt(+10s) = %v, want clamp to 1", got)
	}
	paused := PresenterState{Fraction: 0.3}
	if got := paused.FractionAt(recv, recv.Add(time.Hour)); got != 0.3 {
		t.Fatalf("paused FractionAt = %v, want 0.3", got)
	}
}
