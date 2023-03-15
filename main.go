package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/justinas/alice"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/hlog"
	"github.com/rs/zerolog/log"
)

var (
	logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})
	flags  = struct {
		// Data directory for the cache
		DataDir string
		// Port to listen on
		ListenPort string
	}{}
)

func run() error {
	flag.StringVar(&flags.DataDir, "data-dir", "./data", "Data dir for the cache")
	flag.StringVar(&flags.ListenPort, "port", ":8090", "Port to listen on")
	flag.Parse()

	c := alice.New()

	// Install the logger handler with default output on the console
	c = c.Append(hlog.NewHandler(logger))

	// Install some provided extra handler to set some request's context fields.
	// Thanks to that handler, all our logs will come with some prepopulated fields.
	c = c.Append(hlog.AccessHandler(func(r *http.Request, status, size int, duration time.Duration) {
		hlog.FromRequest(r).Info().
			Str("method", r.Method).
			Stringer("url", r.URL).
			Int("status", status).
			Int("size", size).
			Dur("duration", duration).
			Msg("")
	}))
	c = c.Append(hlog.RemoteAddrHandler("ip"))
	c = c.Append(hlog.UserAgentHandler("user_agent"))
	c = c.Append(hlog.RefererHandler("referer"))
	c = c.Append(hlog.RequestIDHandler("req_id", "Request-Id"))

	// Here is your final handler
	h := c.Then(http.HandlerFunc(handleSourceGo))
	http.Handle("/source/go", h)

	if err := http.ListenAndServe(flags.ListenPort, nil); err != nil {
		return err
	}

	return nil
}

func goEnv() []string {
	cacheDir, err := filepath.Abs(flags.DataDir)
	if err != nil {
		panic(err)
	}

	return []string{
		"GOPATH=" + filepath.Join(cacheDir, "go-mod-cache"),
		"GOCACHE=" + filepath.Join(cacheDir, "go-cache"),
		"GOMODCACHE=" + filepath.Join(cacheDir, "go-mod-cache"),
	}
}

type SourceSpec struct {
	Repo         string
	RelativePath string
	Revision     string
}

func (s *SourceSpec) writeContent(ctx context.Context, revision string, w io.Writer) error {
	if s.Repo == "" {
		return writeStdLibGo(ctx, s.RelativePath, w)
	}

	// if source spec has an empty revision take the on from the user, if that is empty too, use latest
	rev := s.Revision
	if rev == "" {
		rev = revision
	}
	if rev == "" {
		rev = "latest"
	}

	return writeSourceGo(ctx, s.Repo, rev, s.RelativePath, w)

}

func resolveImportPath(function string, path string) (*SourceSpec, error) {
	var (
		spec      SourceSpec
		pathParts = strings.Split(path, "/")
	)

	// check if there is a go mod version specifier (using @) in the path
	for idx := range pathParts {
		if pos := strings.LastIndex(pathParts[idx], "@"); pos >= 0 {
			spec.Revision = pathParts[idx][pos+1:]
			pathParts[idx] = pathParts[idx][:pos]

			// if we got a relative file path we have all information now
			spec.RelativePath = strings.Join(pathParts[idx+1:], "/")
			if !strings.HasPrefix(path, "/") {
				spec.Repo = strings.Join(pathParts[:idx+1], "/")
				return &spec, nil
			}

			//pathParts = pathParts[idx+1:]
			break
		}
	}

	if function == "" {
		return nil, errors.New("function is empty and couldn't be determined, as this is only possible when a relative path is used with a go.mod version specification")
	}

	functionParts := strings.Split(function, "/")

	// find the left most part and remove function name
	// strip of function name from import path
	posDot := strings.Index(functionParts[len(functionParts)-1], ".")
	if posDot >= 0 {
		functionParts[len(functionParts)-1] = functionParts[len(functionParts)-1][:posDot]
	}

	// if the first part doesn't contain a . then it is the standard libary
	if !strings.Contains(functionParts[0], ".") {
		pos := strings.LastIndex(path, functionParts[0])
		if pos >= 0 {
			return &SourceSpec{RelativePath: path[pos:]}, nil
		}
	}

	// when the first elemet is "github.com", expect two more to form repo
	if spec.Repo == "" {
		if len(functionParts) >= 3 {
			spec.Repo = strings.Join(functionParts[:3], "/")
			functionParts = functionParts[3:]
		}
	}

	// when relative path not populated take the package path length from path
	if spec.RelativePath == "" {
		startIndex := len(pathParts) - len(functionParts) - 1
		spec.RelativePath = strings.Join(pathParts[startIndex:], "/")
	}

	return &spec, nil
}

func writeStdLibGo(ctx context.Context, sourcePath string, w io.Writer) error {
	goRoot, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		return err
	}

	fmt.Println(filepath.Join(strings.TrimSpace(string(goRoot)), "src", sourcePath))

	f, err := os.Open(filepath.Join(strings.TrimSpace(string(goRoot)), "src", sourcePath))
	defer f.Close()

	if _, err := io.Copy(w, f); err != nil {
		return err
	}

	return nil

}

func writeSourceGo(ctx context.Context, repo, revision, path string, w io.Writer) error {
	cmd := []string{"go", "mod", "download", "-json", repo + "@" + revision}
	log.Ctx(ctx).Info().
		Strs("cmd", cmd).
		Msg("running command")
	c := exec.Command(cmd[0], cmd[1:]...)
	c.Env = append(os.Environ(), goEnv()...)

	var stdOut, stdErr bytes.Buffer
	c.Stdout = &stdOut
	c.Stderr = &stdErr
	exitErr := c.Run()

	goModInfo := struct {
		Path     string `json:"Path"`
		Version  string `json:"Version"`
		Query    string `json:"Query"`
		Info     string `json:"Info"`
		GoMod    string `json:"GoMod"`
		Zip      string `json:"Zip"`
		Dir      string `json:"Dir"`
		Sum      string `json:"Sum"`
		GoModSum string `json:"GoModSum"`
	}{}

	if err := json.Unmarshal(stdOut.Bytes(), &goModInfo); err != nil {
		return err
	}

	// TODO: Figure out if this should be an error after all
	if exitErr != nil {
		return fmt.Errorf("%+#v: %w", goModInfo, exitErr)
	}

	log.Ctx(ctx).Info().
		Str("repo", goModInfo.Path).
		Str("local-path", goModInfo.Dir).
		Str("version", goModInfo.Version).
		Msg("found go module")

	f, err := os.Open(filepath.Join(goModInfo.Dir, path))
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := io.Copy(w, f); err != nil {
		return err
	}

	return nil
}

func handleSourceGo(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	var (
		function = q.Get("function")
		revision = q.Get("revision")
		path     = q.Get("path")
	)

	if path == "" {
		http.Error(w, "missing path", http.StatusBadRequest)
		return
	}

	if revision == "" {
		revision = "latest"
	}

	spec, err := resolveImportPath(function, path)
	if err != nil {
		http.Error(w, "error resolving import path", http.StatusInternalServerError)
		hlog.FromRequest(req).Error().Err(err).Msg("error resolving import path")
		return
	}

	if err := spec.writeContent(req.Context(), revision, w); err != nil {
		http.Error(w, "error retrieving source code", http.StatusInternalServerError)
		hlog.FromRequest(req).Error().Err(err).Msg("error retrieving source code")
		return
	}
}

func main() {
	if err := run(); err != nil {
		log.Fatal().Err(err).Msg("Error")
	}
}
