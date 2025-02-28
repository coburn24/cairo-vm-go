package integrationtests

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NethermindEth/cairo-vm-go/pkg/runners/zero"
	"github.com/NethermindEth/cairo-vm-go/pkg/vm"
	"github.com/consensys/gnark-crypto/ecc/stark-curve/fp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCairoZeroFiles(t *testing.T) {
	root := "./cairo_files/"
	testFiles, err := os.ReadDir(root)
	require.NoError(t, err)

	// filter is for debugging purposes
	filter := ""

	for _, dirEntry := range testFiles {
		if dirEntry.IsDir() || isGeneratedFile(dirEntry.Name()) {
			continue
		}

		path := filepath.Join(root, dirEntry.Name())

		if !strings.Contains(path, filter) {
			continue
		}
		t.Logf("testing: %s\n", path)

		compiledOutput, err := compileZeroCode(path)
		if err != nil {
			t.Error(err)
			continue
		}

		pyTraceFile, pyMemoryFile, err := runPythonVm(compiledOutput)
		if err != nil {
			t.Error(err)
			continue
		}

		traceFile, memoryFile, err := runVm(compiledOutput)
		if err != nil {
			t.Error(err)
			continue
		}

		pyTrace, pyMemory, err := decodeProof(pyTraceFile, pyMemoryFile)
		if err != nil {
			t.Error(err)
			continue
		}

		trace, memory, err := decodeProof(traceFile, memoryFile)
		if err != nil {
			t.Error(err)
			continue
		}

		if !assert.Equal(t, pyTrace, trace) {
			t.Logf("pytrace:\n%s\n", traceRepr(pyTrace))
			t.Logf("trace:\n%s\n", traceRepr(trace))
		}
		if !assert.Equal(t, pyMemory, memory) {
			t.Logf("pymemory;\n%s\n", memoryRepr(pyMemory))
			t.Logf("memory;\n%s\n", memoryRepr(memory))
		}
	}

	clean(root)
}

const (
	compiledSuffix = "_compiled.json"
	pyTraceSuffix  = "_py_trace"
	pyMemorySuffix = "_py_memory"
	traceSuffix    = "_trace"
	memorySuffix   = "_memory"
)

// given a path to a cairo zero file, it compiles it
// and return the compilation path
func compileZeroCode(path string) (string, error) {
	if filepath.Ext(path) != ".cairo" {
		return "", fmt.Errorf("compiling non cairo file: %s", path)
	}
	compiledOutput := swapExtenstion(path, compiledSuffix)

	cmd := exec.Command(
		"cairo-compile",
		path,
		"--proof_mode",
		"--no_debug_info",
		"--output",
		compiledOutput,
	)

	res, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf(
			"cairo-compile %s: %w\n%s", path, err, string(res),
		)
	}

	return compiledOutput, nil
}

// given a path to a compiled cairo zero file, execute it using the
// python vm and returns the trace and memory files location
func runPythonVm(path string) (string, string, error) {
	traceOutput := swapExtenstion(path, pyTraceSuffix)
	memoryOutput := swapExtenstion(path, pyMemorySuffix)

	cmd := exec.Command(
		"cairo-run",
		"--program",
		path,
		"--proof_mode",
		"--trace_file",
		traceOutput,
		"--memory_file",
		memoryOutput,
	)

	res, err := cmd.CombinedOutput()
	if err != nil {
		return "", "", fmt.Errorf(
			"cairo-run %s: %w\n%s", path, err, string(res),
		)
	}

	return traceOutput, memoryOutput, nil
}

// given a path to a compiled cairo zero file, execute
// it using our vm
func runVm(path string) (string, string, error) {
	traceOutput := swapExtenstion(path, traceSuffix)
	memoryOutput := swapExtenstion(path, memorySuffix)

	cmd := exec.Command(
		"../bin/cairo-vm",
		"run",
		"--proofmode",
		"--tracefile",
		traceOutput,
		"--memoryfile",
		memoryOutput,
		path,
	)

	res, err := cmd.CombinedOutput()
	if err != nil {
		return "", "", fmt.Errorf(
			"cairo-vm run %s: %w\n%s", path, err, string(res),
		)
	}

	return traceOutput, memoryOutput, nil

}

func decodeProof(traceLocation string, memoryLocation string) ([]vm.Trace, []*fp.Element, error) {
	trace, err := os.ReadFile(traceLocation)
	if err != nil {
		return nil, nil, err
	}
	decodedTrace := zero.DecodeTrace(trace)

	memory, err := os.ReadFile(memoryLocation)
	if err != nil {
		return nil, nil, err
	}
	decodedMemory := zero.DecodeMemory(memory)

	return decodedTrace, decodedMemory, nil
}

func clean(root string) {
	err := filepath.Walk(
		root,
		func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			if isGeneratedFile(path) {
				return os.Remove(path)
			}
			return nil
		},
	)

	if err != nil {
		panic(err)
	}
}

// Given a certain file, it swaps its extension with a new suffix
func swapExtenstion(path string, newSuffix string) string {
	dir, name := filepath.Split(path)
	name = name[:len(name)-len(".cairo")]
	name = fmt.Sprintf("%s%s", name, newSuffix)
	return filepath.Join(dir, name)
}

func isGeneratedFile(path string) bool {
	return strings.HasSuffix(path, compiledSuffix) ||
		strings.HasSuffix(path, pyTraceSuffix) ||
		strings.HasSuffix(path, pyMemorySuffix) ||
		strings.HasSuffix(path, traceSuffix) ||
		strings.HasSuffix(path, memorySuffix)
}

func traceRepr(trace []vm.Trace) string {
	repr := make([]string, len(trace))
	for i, ctx := range trace {
		repr[i] = fmt.Sprintf("{pc: %d, ap: %d, fp: %d}", ctx.Pc, ctx.Ap, ctx.Fp)
	}
	return strings.Join(repr, ", ")
}

func memoryRepr(memory []*fp.Element) string {
	repr := make([]string, len(memory))
	for i, felt := range memory {
		repr[i] = felt.Text(10)
	}
	return strings.Join(repr, ", ")

}
