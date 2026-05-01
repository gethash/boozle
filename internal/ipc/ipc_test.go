package ipc

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPresenterStateAndCommandRoundTrip(t *testing.T) {
	socketPath := filepath.Join(os.TempDir(), fmt.Sprintf("boozle-ipc-test-%d.sock", time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.Remove(socketPath) })
	srv, err := Listen(socketPath)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer srv.Close()
	go srv.AcceptLoop()

	disconnected := make(chan struct{})
	recv, err := Connect(socketPath, func() { close(disconnected) })
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	wantState := PresenterState{
		Page:           4,
		ListIndex:      2,
		Total:          9,
		Fraction:       0.75,
		Paused:         true,
		NextPage:       5,
		ElapsedSeconds: 123,
	}

	deadline := time.Now().Add(time.Second)
	for {
		srv.Send(wantState)
		got := recv.Latest()
		if got == wantState {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("receiver latest = %+v, want %+v", got, wantState)
		}
		time.Sleep(10 * time.Millisecond)
	}

	wantCmd := PresenterCommand{Name: "digit", Arg: 7}
	if ok := recv.SendCommand(wantCmd); !ok {
		t.Fatal("SendCommand returned false")
	}
	select {
	case got := <-srv.Commands():
		if got != wantCmd {
			t.Fatalf("command = %+v, want %+v", got, wantCmd)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for command")
	}

	_ = srv.Close()
	select {
	case <-disconnected:
	case <-time.After(time.Second):
		t.Fatal("receiver did not observe disconnect")
	}
}
