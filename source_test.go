package main

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_resolveImportPath(t *testing.T) {
	for _, tc := range []struct {
		name         string
		functionName string
		sourcePath   string

		expRepo     string
		expPath     string
		expRevision string
	}{
		{
			name:         "stdlib",
			functionName: "runtime.gopark",
			sourcePath:   "/nix/store/v6i0a6bfx3707airawpc2589pbbl465r-go-1.19.5/share/go/src/runtime/proc.go",

			expPath: "runtime/proc.go",
		},
		{
			name:         "stdlib-more-path-segments",
			functionName: "compress/gzip.(*Writer).Reset",
			sourcePath:   "/opt/hostedtoolcache/go/1.19.6/x64/src/compress/gzip/gzip.go",

			expPath: "compress/gzip/gzip.go",
		},
		{
			name:         "go-mod-abs-path",
			functionName: "github.com/felixge/httpsnoop.(*Metrics).CaptureMetrics",
			sourcePath:   "/home/runner/go/pkg/mod/github.com/felixge/httpsnoop@v1.0.3/capture_metrics.go",

			expRepo:     "github.com/felixge/httpsnoop",
			expPath:     "capture_metrics.go",
			expRevision: "v1.0.3",
		},
		{
			name:         "go-mod-relative",
			functionName: "sigs.k8s.io/controller-runtime/pkg/internal/controller.(*Controller).processNextWorkItem",
			sourcePath:   "sigs.k8s.io/controller-runtime@v0.13.1/pkg/internal/controller/controller.go",

			expRepo:     "sigs.k8s.io/controller-runtime",
			expPath:     "pkg/internal/controller/controller.go",
			expRevision: "v0.13.1",
		},
		{
			name:       "go-mod-relative-no-revision",
			sourcePath: "github.com/aws/aws-sdk-go@v1.44.163/aws/endpoints/defaults.go",

			expRepo:     "github.com/aws/aws-sdk-go",
			expPath:     "aws/endpoints/defaults.go",
			expRevision: "v1.44.163",
		},
		{
			name:         "go-main", // This is how a non dependency is looking like (in this case Grafana Phlare)
			functionName: "github.com/grafana/phlare/pkg/phlaredb.(*profileStore).cutRowGroup",
			sourcePath:   "/home/runner/work/phlare/phlare/pkg/phlaredb/profile_store.go",

			expRepo: "github.com/grafana/phlare",
			expPath: "pkg/phlaredb/profile_store.go",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := resolveImportPath(tc.functionName, tc.sourcePath)
			assert.NoError(t, err)
			assert.Equal(t, tc.expRepo, spec.Repo)
			assert.Equal(t, tc.expPath, spec.RelativePath)
			assert.Equal(t, tc.expRevision, spec.Revision)

			ctx := context.Background()
			err = spec.writeContent(ctx, "", os.Stdout)
			//	o.Discard)
			assert.NoError(t, err)

		})
	}
}
