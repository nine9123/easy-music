package radio

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"testing"
)

// shoutcast serves one canned raw response per connection, like a SHOUTcast
// server that answers with an "ICY" status line.
func shoutcast(t *testing.T, resp string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				http.ReadRequest(bufio.NewReader(c))
				io.WriteString(c, resp)
			}()
		}
	}()
	return "http://" + ln.Addr().String() + "/"
}

func TestDialICY(t *testing.T) {
	hc := &http.Client{Transport: &http.Transport{DialContext: DialICY((&net.Dialer{}).DialContext)}}

	u := shoutcast(t, "ICY 200 OK\r\nicy-name:Test FM\r\ncontent-type:audio/mpeg\r\n\r\nSTREAMDATA")
	resp, err := hc.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Icy-Name") != "Test FM" || string(body) != "STREAMDATA" {
		t.Errorf("ICY 200: %d %v %q", resp.StatusCode, resp.Header, body)
	}

	// An offline SHOUTcast v2 station.
	u = shoutcast(t, "ICY 401 Service Unavailable\r\nicy-notice2:The resource requested is currently unavailable<BR>\r\n\r\n")
	resp, err = hc.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("ICY 401: %d", resp.StatusCode)
	}

	// Plain HTTP is untouched.
	u = shoutcast(t, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
	resp, err = hc.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "ok" {
		t.Errorf("HTTP: %q", body)
	}
}
