package health

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProbes(t *testing.T) {
	state := &State{}
	srv := httptest.NewServer(Handler(state))
	t.Cleanup(srv.Close)

	if got := get(t, srv.URL+"/healthz"); got != http.StatusOK {
		t.Fatalf("liveness before ready = %d", got)
	}
	if got := get(t, srv.URL+"/readyz"); got != http.StatusServiceUnavailable {
		t.Fatalf("readiness before ready = %d", got)
	}

	state.SetReady(true)
	if got := get(t, srv.URL+"/readyz"); got != http.StatusOK {
		t.Fatalf("readiness = %d", got)
	}
	body := getBody(t, srv.URL+"/")
	if !strings.Contains(body, "ready") {
		t.Fatalf("status = %q", body)
	}

	state.SetError(errors.New("receive failed"))
	if got := get(t, srv.URL+"/healthz"); got != http.StatusServiceUnavailable {
		t.Fatalf("liveness after error = %d", got)
	}
	if got := get(t, srv.URL+"/readyz"); got != http.StatusServiceUnavailable {
		t.Fatalf("readiness after error = %d", got)
	}
}

func get(t *testing.T, url string) int {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	return res.StatusCode
}

func getBody(t *testing.T, url string) string {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	buf := make([]byte, 128)
	n, _ := res.Body.Read(buf)
	return string(buf[:n])
}
