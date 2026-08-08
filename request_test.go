package gojenkins

// Unit tests for the Requester error handling. Unlike the integration tests in
// jenkins_test.go (which need a live Jenkins and are gated on the
// integration_test environment variable), these run against httptest servers
// and always execute in plain `go test`.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// newTestJenkins returns a Jenkins client pointed at an httptest server that
// serves the given handler. The caller owns neither: both are cleaned up with
// the test.
func newTestJenkins(t *testing.T, handler http.HandlerFunc) *Jenkins {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return CreateJenkins(nil, server.URL, "user", "apitoken")
}

func TestDoReturnsStatusErrorOnHTTPError(t *testing.T) {
	j := newTestJenkins(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("<!DOCTYPE html><html><title>Not Found - Jenkins</title></html>"))
	})

	data := map[string]string{}
	response, err := j.Requester.GetJSON(context.Background(), "/queue/item/123", &data, nil)

	assert.Error(t, err)
	var statusErr *StatusError
	assert.True(t, errors.As(err, &statusErr), "error should be a *StatusError, got %T: %v", err, err)
	assert.Equal(t, http.StatusNotFound, statusErr.StatusCode)
	assert.Contains(t, statusErr.Error(), "404")
	// The response is returned alongside the error so legacy callers that
	// ignore the error and inspect StatusCode directly keep working.
	assert.NotNil(t, response)
	assert.Equal(t, http.StatusNotFound, response.StatusCode)
	// The HTML error page must not have been "decoded" into the struct.
	assert.Empty(t, data)
}

func TestDoAcceptsEmptyBody(t *testing.T) {
	j := newTestJenkins(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})

	// Build-trigger style call: POST with a nil response struct and no body in
	// the response. This must not error (io.EOF from the JSON decoder).
	response, err := j.Requester.Post(context.Background(), "/job/foo/build", nil, nil, nil)

	assert.NoError(t, err)
	assert.Equal(t, http.StatusCreated, response.StatusCode)
}

func TestDoRejectsNonJSONBody(t *testing.T) {
	j := newTestJenkins(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html>login page</html>"))
	})

	data := map[string]string{}
	_, err := j.Requester.GetJSON(context.Background(), "/api", &data, nil)

	assert.Error(t, err, "an HTML body with status 200 must surface as a decode error, not silently produce a zero-valued struct")
	assert.Contains(t, err.Error(), "decode")
}

func TestDoDecodesValidJSON(t *testing.T) {
	j := newTestJenkins(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"crumb":"abc123","crumbRequestField":"Jenkins-Crumb"}`))
	})

	data := map[string]string{}
	response, err := j.Requester.GetJSON(context.Background(), "/crumbIssuer", &data, nil)

	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, response.StatusCode)
	assert.Equal(t, "abc123", data["crumb"])
}

func TestSetCrumbToleratesDisabledCrumbIssuer(t *testing.T) {
	// Jenkins responds 404 on /crumbIssuer/api/json when CSRF protection is
	// disabled; SetCrumb must treat that as "no crumb needed", not an error.
	j := newTestJenkins(t, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})

	ar := NewAPIRequest("POST", "/job/foo/build", nil)
	err := j.Requester.SetCrumb(context.Background(), ar)

	assert.NoError(t, err)
	assert.Empty(t, ar.Headers.Get("Jenkins-Crumb"))
}

func TestSetCrumbSetsHeaderWhenEnabled(t *testing.T) {
	j := newTestJenkins(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"crumb":"abc123","crumbRequestField":"Jenkins-Crumb"}`))
	})

	ar := NewAPIRequest("POST", "/job/foo/build", nil)
	err := j.Requester.SetCrumb(context.Background(), ar)

	assert.NoError(t, err)
	assert.Equal(t, "abc123", ar.Headers.Get("Jenkins-Crumb"))
}
