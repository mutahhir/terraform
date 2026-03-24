// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package runbookplanfile

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/terraform/internal/states/statefile"
)

const (
	manifestFilename = "runbook.json"
	stateFilename    = "tfstate"
	sourcesDirname   = "sources"
	formatVersion    = 1
)

type Plan struct {
	Version    int      `json:"version"`
	ConfigPath string   `json:"config_path"`
	Workspace  string   `json:"workspace"`
	CreatedAt  string   `json:"created_at"`
	StepOrder  []string `json:"step_order"`
	Steps      []Step   `json:"steps"`
}

type Step struct {
	Name           string   `json:"name"`
	BaseName       string   `json:"base_name,omitempty"`
	ForEachKey     string   `json:"for_each_key,omitempty"`
	InstanceCount  int      `json:"instance_count,omitempty"`
	After          []string `json:"after,omitempty"`
	KnownSkipped   bool     `json:"known_skipped,omitempty"`
	SkipReason     string   `json:"skip_reason,omitempty"`
	PlannedActions []string `json:"planned_actions,omitempty"`
	PlannedQueries []string `json:"planned_queries,omitempty"`
	PlannedData    []string `json:"planned_data,omitempty"`
	Outputs        []string `json:"outputs,omitempty"`
}

type CreateArgs struct {
	Plan      *Plan
	StateFile *statefile.File
	Lowered   map[string]map[string][]byte
	Sources   map[string][]byte
}

type Reader struct {
	zip *zip.ReadCloser
}

func Create(filename string, args CreateArgs) error {
	if args.Plan == nil {
		return fmt.Errorf("runbook plan is required")
	}
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		return err
	}
	f, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	defer zw.Close()

	planCopy := *args.Plan
	planCopy.Version = formatVersion
	manifestBytes, err := json.MarshalIndent(&planCopy, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFile(zw, manifestFilename, append(manifestBytes, '\n')); err != nil {
		return err
	}
	if args.StateFile != nil {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: stateFilename, Method: zip.Deflate, Modified: time.Now()})
		if err != nil {
			return err
		}
		if err := statefile.Write(args.StateFile, w); err != nil {
			return err
		}
	}

	stepNames := make([]string, 0, len(args.Lowered))
	for stepName := range args.Lowered {
		stepNames = append(stepNames, stepName)
	}
	sort.Strings(stepNames)
	for _, stepName := range stepNames {
		files := args.Lowered[stepName]
		fileNames := make([]string, 0, len(files))
		for name := range files {
			fileNames = append(fileNames, name)
		}
		sort.Strings(fileNames)
		for _, name := range fileNames {
			if err := writeFile(zw, filepath.ToSlash(filepath.Join("steps", stepName, name)), files[name]); err != nil {
				return err
			}
		}
	}

	sourceNames := make([]string, 0, len(args.Sources))
	for name := range args.Sources {
		sourceNames = append(sourceNames, name)
	}
	sort.Strings(sourceNames)
	for _, name := range sourceNames {
		if err := writeFile(zw, filepath.ToSlash(filepath.Join(sourcesDirname, name)), args.Sources[name]); err != nil {
			return err
		}
	}

	return nil
}

func Open(filename string) (*Reader, error) {
	r, err := zip.OpenReader(filename)
	if err != nil {
		return nil, err
	}
	var manifest *zip.File
	for _, file := range r.File {
		if file.Name == manifestFilename {
			manifest = file
			break
		}
	}
	if manifest == nil {
		r.Close()
		return nil, fmt.Errorf("the given file is not a valid runbook plan file")
	}
	return &Reader{zip: r}, nil
}

func (r *Reader) ReadPlan() (*Plan, error) {
	for _, file := range r.zip.File {
		if file.Name != manifestFilename {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		src, err := io.ReadAll(rc)
		if err != nil {
			return nil, err
		}
		var plan Plan
		if err := json.Unmarshal(src, &plan); err != nil {
			return nil, err
		}
		if plan.Version != formatVersion {
			return nil, fmt.Errorf("unsupported runbook plan format version %d", plan.Version)
		}
		return &plan, nil
	}
	return nil, fmt.Errorf("runbook plan manifest missing")
}

func (r *Reader) ReadStateFile() (*statefile.File, error) {
	for _, file := range r.zip.File {
		if file.Name != stateFilename {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return statefile.Read(rc)
	}
	return nil, statefile.ErrNoState
}

func (r *Reader) ReadLoweredStepFiles(stepName string) (map[string][]byte, error) {
	prefix := filepath.ToSlash(filepath.Join("steps", stepName)) + "/"
	ret := make(map[string][]byte)
	for _, file := range r.zip.File {
		if !strings.HasPrefix(file.Name, prefix) {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			return nil, err
		}
		src, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, err
		}
		ret[strings.TrimPrefix(file.Name, prefix)] = src
	}
	return ret, nil
}

func (r *Reader) ReadSourceFiles() (map[string][]byte, error) {
	prefix := sourcesDirname + "/"
	ret := make(map[string][]byte)
	for _, file := range r.zip.File {
		if !strings.HasPrefix(file.Name, prefix) {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			return nil, err
		}
		src, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, err
		}
		ret[strings.TrimPrefix(file.Name, prefix)] = src
	}
	return ret, nil
}

func (r *Reader) Close() error {
	return r.zip.Close()
}

func writeFile(zw *zip.Writer, name string, content []byte) error {
	w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: time.Now()})
	if err != nil {
		return err
	}
	_, err = w.Write(content)
	return err
}
