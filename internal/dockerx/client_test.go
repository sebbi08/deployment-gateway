package dockerx

import (
	"context"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/moby/moby/client"
)

// Logs asks the daemon for the requested tail only (not the whole log) and
// keeps at most maxBytes of the newest output.
func TestLogsSendsTailAndKeepsNewestBytes(t *testing.T) {
	var query url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/containers/abc/logs") {
			http.NotFound(w, r)
			return
		}
		query = r.URL.Query()
		for i := range 100 { // stdcopy frames: stream 1 (stdout), big-endian length
			line := fmt.Sprintf("line %02d\n", i)
			hdr := make([]byte, 8)
			hdr[0] = 1
			binary.BigEndian.PutUint32(hdr[4:], uint32(len(line)))
			w.Write(append(hdr, line...))
		}
	}))
	defer srv.Close()
	api, err := client.New(client.WithHost("tcp://"+srv.Listener.Addr().String()), client.WithAPIVersion("1.47"))
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{api: api}
	out, err := c.Logs(context.Background(), "abc", 7, "", 40)
	if err != nil {
		t.Fatal(err)
	}
	if got := query.Get("tail"); got != "7" {
		t.Fatalf("tail query = %q, want 7 (all params: %v)", got, query)
	}
	if !strings.HasSuffix(out, "line 98\nline 99\n") || len(out) > 40+len(truncatedMark) {
		t.Fatalf("output = %q", out)
	}
}
