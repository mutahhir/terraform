package runbookplanfile

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/hashicorp/terraform/internal/plans"
)

const (
	tfrunplanFilename        = "tfrunplan"
	tfrunstateFilename       = "tfrunstate"
	tfrunlockFilename        = "lock.hcl"
	terraformLockFilename    = "terraform.lock.hcl"
	tfrunconfigPrefix        = "tfrunconfig/"
	tfrunworkspacePrefix     = "tfrunworkspace/"
)

// ErrUnusablePlan indicates the file looks like a plan but can't be used.
type ErrUnusablePlan struct {
	inner error
}

func (e *ErrUnusablePlan) Error() string { return e.inner.Error() }
func (e *ErrUnusablePlan) Unwrap() error { return e.inner }

// Read opens a runbook plan file and deserializes the plan.
func Read(path string) (*Plan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("failed to open plan file: %w", err)
	}

	// Find and read the main plan entry
	var planFile *zip.File
	for _, f := range r.File {
		if f.Name == tfrunplanFilename {
			planFile = f
			break
		}
	}
	if planFile == nil {
		return nil, &ErrUnusablePlan{inner: fmt.Errorf("not a valid runbook plan file (missing %s entry)", tfrunplanFilename)}
	}

	pr, err := planFile.Open()
	if err != nil {
		return nil, fmt.Errorf("failed to read plan entry: %w", err)
	}
	defer pr.Close()

	planBytes, err := io.ReadAll(pr)
	if err != nil {
		return nil, fmt.Errorf("failed to read plan data: %w", err)
	}

	var plan Plan
	if err := json.Unmarshal(planBytes, &plan); err != nil {
		return nil, &ErrUnusablePlan{inner: fmt.Errorf("corrupt plan data: %w", err)}
	}

	if plan.Version != FormatVersion {
		return nil, &ErrUnusablePlan{inner: fmt.Errorf(
			"unsupported runbook plan format version %d; this binary supports version %d",
			plan.Version, FormatVersion,
		)}
	}

	// Read separate entries
	plan.WorkspaceStateFile = readZipEntry(r, tfrunstateFilename)
	plan.RunbookLockFile = readZipEntry(r, tfrunlockFilename)
	plan.TerraformLockFile = readZipEntry(r, terraformLockFilename)
	plan.Sources = readZipDir(r, tfrunconfigPrefix)
	plan.WorkspaceSources = readZipDir(r, tfrunworkspacePrefix)

	// Defaults
	if plan.Sources == nil {
		plan.Sources = map[string][]byte{}
	}
	if plan.WorkspaceSources == nil {
		plan.WorkspaceSources = map[string][]byte{}
	}
	if plan.Variables == nil {
		plan.Variables = map[string]plans.DynamicValue{}
	}

	return &plan, nil
}

// Write serializes a plan to a zip-based plan file.
func Write(path string, plan *Plan) error {
	if plan == nil {
		return fmt.Errorf("nil runbook plan")
	}
	if plan.Version == 0 {
		plan.Version = FormatVersion
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	defer zw.Close()

	// Write the main plan payload (JSON, without inline blobs)
	{
		planCopy := *plan
		// These are written as separate entries, clear them from the JSON payload
		planCopy.WorkspaceStateFile = nil
		planCopy.RunbookLockFile = nil
		planCopy.TerraformLockFile = nil
		planCopy.Sources = nil
		planCopy.WorkspaceSources = nil

		data, err := json.Marshal(&planCopy)
		if err != nil {
			return fmt.Errorf("failed to marshal plan: %w", err)
		}
		if err := writeZipEntry(zw, tfrunplanFilename, data); err != nil {
			return err
		}
	}

	// Workspace state
	if len(plan.WorkspaceStateFile) > 0 {
		if err := writeZipEntry(zw, tfrunstateFilename, plan.WorkspaceStateFile); err != nil {
			return err
		}
	}

	// Runbook lock
	if len(plan.RunbookLockFile) > 0 {
		if err := writeZipEntry(zw, tfrunlockFilename, plan.RunbookLockFile); err != nil {
			return err
		}
	}

	// Terraform lock
	if len(plan.TerraformLockFile) > 0 {
		if err := writeZipEntry(zw, terraformLockFilename, plan.TerraformLockFile); err != nil {
			return err
		}
	}

	// Source files
	for name, content := range plan.Sources {
		if err := writeZipEntry(zw, tfrunconfigPrefix+name, content); err != nil {
			return err
		}
	}

	// Workspace source files
	for name, content := range plan.WorkspaceSources {
		if err := writeZipEntry(zw, tfrunworkspacePrefix+name, content); err != nil {
			return err
		}
	}

	return nil
}

func writeZipEntry(zw *zip.Writer, name string, data []byte) error {
	w, err := zw.CreateHeader(&zip.FileHeader{
		Name:     name,
		Method:   zip.Deflate,
		Modified: time.Now(),
	})
	if err != nil {
		return fmt.Errorf("failed to create %s entry: %w", name, err)
	}
	_, err = w.Write(data)
	if err != nil {
		return fmt.Errorf("failed to write %s entry: %w", name, err)
	}
	return nil
}

func readZipEntry(r *zip.Reader, name string) []byte {
	for _, f := range r.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				return nil
			}
			defer rc.Close()
			data, _ := io.ReadAll(rc)
			return data
		}
	}
	return nil
}

func readZipDir(r *zip.Reader, prefix string) map[string][]byte {
	result := map[string][]byte{}
	for _, f := range r.File {
		if len(f.Name) > len(prefix) && f.Name[:len(prefix)] == prefix {
			name := f.Name[len(prefix):]
			rc, err := f.Open()
			if err != nil {
				continue
			}
			data, _ := io.ReadAll(rc)
			rc.Close()
			result[name] = data
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}
