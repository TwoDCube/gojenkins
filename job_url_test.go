package gojenkins

// Unit tests for jobURLPath and for Job.GetBuild resolving builds when the
// server advertises job URLs under a different host than the client connects
// through (reverse proxies, tunnels, split-horizon DNS). These run against
// httptest servers and are not gated on the integration_test environment
// variable.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestJobURLPath(t *testing.T) {
	tests := []struct {
		name      string
		jobURL    string
		serverURL string
		want      string
	}{
		{
			name:      "job URL host matches the server",
			jobURL:    "https://jenkins.example.com/job/foo/",
			serverURL: "https://jenkins.example.com",
			want:      "/job/foo",
		},
		{
			name:      "job URL advertises a foreign host (tunnel/proxy)",
			jobURL:    "https://public.example.com/job/Containers-Runtime/job/kube-e2e-pvg/",
			serverURL: "http://jenkins-tunnel.ns.svc.cluster.local:8080",
			want:      "/job/Containers-Runtime/job/kube-e2e-pvg",
		},
		{
			name:      "server URL carries a path prefix",
			jobURL:    "https://host.example.com/jenkins/job/foo/",
			serverURL: "https://host.example.com/jenkins",
			want:      "/job/foo",
		},
		{
			name:      "foreign host and server path prefix together",
			jobURL:    "https://public.example.com/jenkins/job/foo/",
			serverURL: "http://127.0.0.1:8080/jenkins",
			want:      "/job/foo",
		},
		{
			name:      "no trailing slash on the job URL",
			jobURL:    "https://jenkins.example.com/job/foo",
			serverURL: "https://jenkins.example.com",
			want:      "/job/foo",
		},
		{
			name:      "nested folder job",
			jobURL:    "https://jenkins.example.com/job/a/job/b/job/c/",
			serverURL: "https://jenkins.example.com",
			want:      "/job/a/job/b/job/c",
		},
		{
			name:      "unparseable job URL falls back to prefix strip",
			jobURL:    "http://bad url/job/foo/",
			serverURL: "http://bad url",
			want:      "/job/foo/",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, jobURLPath(tt.jobURL, tt.serverURL))
		})
	}
}

// TestGetBuildWithForeignJobURL reproduces the tunnel scenario: the client
// connects through one URL while Jenkins reports the job under its configured
// public hostname. GetBuild must hit the connection URL, not the advertised
// one.
func TestGetBuildWithForeignJobURL(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/job/Containers-Runtime/job/kube-e2e-pvg/185146/api/json",
		func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"number":185146,"result":"SUCCESS","building":false}`)
		})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	j := CreateJenkins(nil, server.URL, "user", "apitoken")
	job := &Job{
		Jenkins: j,
		Raw: &JobResponse{
			Name: "kube-e2e-pvg",
			URL:  "https://alchemy-containers-jenkins.example.com/job/Containers-Runtime/job/kube-e2e-pvg/",
		},
		Base: "/job/Containers-Runtime/job/kube-e2e-pvg",
	}

	build, err := job.GetBuild(context.Background(), 185146)
	assert.NoError(t, err)
	if assert.NotNil(t, build) {
		assert.Equal(t, int64(185146), build.GetBuildNumber())
		assert.Equal(t, "SUCCESS", build.GetResult())
	}
}
