package imapd

import (
	"bufio"
	"net"
	"strings"
	"testing"
	"time"
)

func TestLimitListenerPerIP(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := newLimitListener(raw, 10, 2)
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_, _ = c.Write([]byte("* OK hi\r\n"))
			go func() { time.Sleep(2 * time.Second); c.Close() }()
		}
	}()
	greet := func() string {
		c, err := net.Dial("tcp", raw.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		line, _ := bufio.NewReader(c).ReadString('\n')
		return line
	}
	for i := 0; i < 2; i++ {
		if l := greet(); !strings.HasPrefix(l, "* OK") {
			t.Fatalf("conn %d: %q", i, l)
		}
	}
	if l := greet(); !strings.HasPrefix(l, "* BYE") {
		t.Fatalf("third conn from same IP: %q", l)
	}
	time.Sleep(2500 * time.Millisecond) // the first two are closed and released
	if l := greet(); !strings.HasPrefix(l, "* OK") {
		t.Fatalf("after release: %q", l)
	}
}
