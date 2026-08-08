package gojenkins

// Unit tests for resolving queue items to builds: TryGetBuildFromQueueID,
// Job.FindBuildByQueueID, and the context-cancellation behavior of the
// blocking GetBuildFromQueueID. These run against httptest servers and are not
// gated on the integration_test environment variable.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// newQueueTestServer serves a minimal fake of the Jenkins endpoints used by
// queue resolution:
//   - /queue/item/<id>/api/json  -> queueItemJSON ("" means respond 404,
//     mimicking a garbage-collected queue item)
//   - /job/foo/api/json          -> job JSON, honoring the tree query used by
//     FindBuildByQueueID (buildsJSON)
//   - /job/foo/<n>/api/json      -> a build with that number
//
// It returns the Jenkins client and a Job wired to /job/foo.
func newQueueTestServer(t *testing.T, queueItemJSON, buildsJSON string) (*Jenkins, *Job) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/queue/item/", func(w http.ResponseWriter, r *http.Request) {
		if queueItemJSON == "" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(queueItemJSON))
	})
	mux.HandleFunc("/job/foo/api/json", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(buildsJSON))
	})
	mux.HandleFunc("/job/foo/", func(w http.ResponseWriter, r *http.Request) {
		var number int64
		if _, err := fmt.Sscanf(r.URL.Path, "/job/foo/%d/api/json", &number); err != nil {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `{"number":%d,"result":"SUCCESS","building":false}`, number)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	j := CreateJenkins(nil, server.URL, "user", "apitoken")
	job := &Job{
		Jenkins: j,
		Raw:     &JobResponse{Name: "foo", URL: server.URL + "/job/foo"},
		Base:    "/job/foo",
	}
	return j, job
}

func TestTryGetBuildFromQueueIDStillQueued(t *testing.T) {
	j, job := newQueueTestServer(t, `{"id":5,"why":"Waiting for next available executor"}`, `{"builds":[]}`)

	build, err := j.TryGetBuildFromQueueID(context.Background(), job, 5)

	assert.NoError(t, err)
	assert.Nil(t, build, "a queue item without an executable must resolve to a nil build, not block")
}

func TestTryGetBuildFromQueueIDStarted(t *testing.T) {
	j, job := newQueueTestServer(t, `{"id":5,"executable":{"number":42}}`, `{"builds":[]}`)

	build, err := j.TryGetBuildFromQueueID(context.Background(), job, 5)

	assert.NoError(t, err)
	assert.NotNil(t, build)
	assert.Equal(t, int64(42), build.GetBuildNumber())
}

func TestTryGetBuildFromQueueIDGarbageCollected(t *testing.T) {
	j, job := newQueueTestServer(t, "", `{"builds":[]}`)

	build, err := j.TryGetBuildFromQueueID(context.Background(), job, 5)

	assert.Nil(t, build)
	var statusErr *StatusError
	assert.True(t, errors.As(err, &statusErr), "a GC'd queue item must surface as *StatusError, got %T: %v", err, err)
	assert.Equal(t, http.StatusNotFound, statusErr.StatusCode)
}

func TestFindBuildByQueueIDFound(t *testing.T) {
	builds := `{"builds":[{"number":44,"queueId":9000},{"number":43,"queueId":8999},{"number":42,"queueId":8998}]}`
	_, job := newQueueTestServer(t, "", builds)

	build, err := job.FindBuildByQueueID(context.Background(), 8999, 50)

	assert.NoError(t, err)
	assert.NotNil(t, build)
	assert.Equal(t, int64(43), build.GetBuildNumber())
}

func TestFindBuildByQueueIDNotFound(t *testing.T) {
	builds := `{"builds":[{"number":44,"queueId":9000}]}`
	_, job := newQueueTestServer(t, "", builds)

	build, err := job.FindBuildByQueueID(context.Background(), 1234, 50)

	assert.NoError(t, err)
	assert.Nil(t, build, "an unknown queue ID must resolve to (nil, nil), not an error")
}

func TestFindBuildByQueueIDSendsTreeQuery(t *testing.T) {
	var gotTree string
	mux := http.NewServeMux()
	mux.HandleFunc("/job/foo/api/json", func(w http.ResponseWriter, r *http.Request) {
		gotTree = r.URL.Query().Get("tree")
		w.Write([]byte(`{"builds":[]}`))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	j := CreateJenkins(nil, server.URL, "user", "apitoken")
	job := &Job{Jenkins: j, Raw: &JobResponse{Name: "foo", URL: server.URL + "/job/foo"}, Base: "/job/foo"}

	// A non-positive limit falls back to the documented default of 50.
	_, err := job.FindBuildByQueueID(context.Background(), 1, 0)

	assert.NoError(t, err)
	assert.Equal(t, "builds[number,queueId]{0,50}", gotTree)
}

func TestGetBuildFromQueueIDHonorsContextCancellation(t *testing.T) {
	// The queue item never gets an executable, so the blocking variant would
	// previously spin forever; it must now stop when the context is cancelled.
	j, job := newQueueTestServer(t, `{"id":5,"why":"Waiting for next available executor"}`, `{"builds":[]}`)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := j.GetBuildFromQueueID(ctx, job, 5)
		done <- err
	}()

	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(5 * time.Second):
		t.Fatal("GetBuildFromQueueID did not return after context cancellation")
	}
}
