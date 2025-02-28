package zero

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/NethermindEth/cairo-vm-go/pkg/hintrunner"
	"github.com/NethermindEth/cairo-vm-go/pkg/safemath"
	"github.com/NethermindEth/cairo-vm-go/pkg/vm"
	VM "github.com/NethermindEth/cairo-vm-go/pkg/vm"
	"github.com/NethermindEth/cairo-vm-go/pkg/vm/memory"
	f "github.com/consensys/gnark-crypto/ecc/stark-curve/fp"
)

type ZeroRunner struct {
	memoryManager *memory.MemoryManager
	// core components
	program    *Program
	vm         *VM.VirtualMachine
	hintrunner hintrunner.HintRunner
	// config
	proofmode bool
	maxsteps  uint64
	// auxiliar
	runFinished bool
}

// Creates a new Runner of a Cairo Zero program
func NewRunner(program *Program, proofmode bool, maxsteps uint64) (*ZeroRunner, error) {
	memoryManager := memory.CreateMemoryManager()
	_, err := memoryManager.Memory.AllocateSegment(program.Bytecode) // ProgramSegment
	if err != nil {
		return nil, err
	}
	memoryManager.Memory.AllocateEmptySegment() // ExecutionSegment

	// initialize vm
	vm, err := VM.NewVirtualMachine(vm.Context{}, memoryManager.Memory, vm.VirtualMachineConfig{ProofMode: proofmode})
	if err != nil {
		return nil, fmt.Errorf("runner error: %w", err)
	}

	// todo(rodro): given the program get the appropiate hints
	hintrunner := hintrunner.NewHintRunner(make(map[uint64]hintrunner.Hinter))

	return &ZeroRunner{
		memoryManager: memoryManager,
		program:       program,
		vm:            vm,
		hintrunner:    hintrunner,
		proofmode:     proofmode,
		maxsteps:      maxsteps,
	}, nil
}

// todo(rodro): should we add support for running any function?
func (runner *ZeroRunner) Run() error {
	if runner.runFinished {
		return errors.New("cannot re-run using the same runner")
	}

	end, err := runner.InitializeMainEntrypoint()
	if err != nil {
		return fmt.Errorf("initializing main entry point: %w", err)
	}

	err = runner.RunUntilPc(&end)
	if err != nil {
		return err
	}

	if runner.proofmode {
		// proof mode require an extra instruction run
		if err := runner.RunFor(1); err != nil {
			return err
		}

		// proof mode also requires that the trace is a power of two
		pow2Steps := safemath.NextPowerOfTwo(runner.vm.Step)
		if err := runner.RunFor(pow2Steps); err != nil {
			return err
		}
	}
	return nil
}

func (runner *ZeroRunner) InitializeMainEntrypoint() (memory.MemoryAddress, error) {
	if runner.proofmode {
		startPc, ok := runner.program.Labels["__start__"]
		if !ok {
			return memory.UnknownValue, errors.New("start label not found. Try compiling with `--proof_mode`")
		}
		endPc, ok := runner.program.Labels["__end__"]
		if !ok {
			return memory.UnknownValue, errors.New("end label not found. Try compiling with `--proof_mode`")
		}

		offset := runner.segments()[VM.ExecutionSegment].Len()

		dummyFPValue := memory.MemoryValueFromSegmentAndOffset(
			VM.ProgramSegment,
			runner.segments()[VM.ProgramSegment].Len()+offset+2,
		)
		// set dummy fp value
		err := runner.memory().Write(
			VM.ExecutionSegment,
			offset,
			&dummyFPValue,
		)
		if err != nil {
			return memory.UnknownValue, err
		}

		dummyPCValue := memory.MemoryValueFromUint[uint64](0)
		// set dummy pc value
		err = runner.memory().Write(VM.ExecutionSegment, offset+1, &dummyPCValue)
		if err != nil {
			return memory.UnknownValue, err
		}

		runner.vm.Context.Pc = memory.MemoryAddress{SegmentIndex: VM.ProgramSegment, Offset: startPc}
		runner.vm.Context.Ap = offset + 2
		runner.vm.Context.Fp = runner.vm.Context.Ap
		return memory.MemoryAddress{SegmentIndex: VM.ProgramSegment, Offset: endPc}, nil
	}

	returnFp := memory.MemoryValueFromSegmentAndOffset(
		runner.memory().AllocateEmptySegment(),
		0,
	)
	return runner.InitializeEntrypoint("main", nil, &returnFp)
}

func (runner *ZeroRunner) InitializeEntrypoint(
	funcName string, arguments []*f.Element, returnFp *memory.MemoryValue,
) (memory.MemoryAddress, error) {
	segmentIndex := runner.memory().AllocateEmptySegment()
	end := memory.MemoryAddress{SegmentIndex: uint64(segmentIndex), Offset: 0}
	// write arguments
	for i := range arguments {
		v := memory.MemoryValueFromFieldElement(arguments[i])
		err := runner.memory().Write(VM.ExecutionSegment, uint64(i), &v)
		if err != nil {
			return memory.UnknownValue, err
		}
	}
	offset := runner.segments()[VM.ExecutionSegment].Len()
	err := runner.memory().Write(VM.ExecutionSegment, offset, returnFp)
	if err != nil {
		return memory.UnknownValue, err
	}
	endMV := memory.MemoryValueFromMemoryAddress(&end)
	err = runner.memory().Write(VM.ExecutionSegment, offset+1, &endMV)
	if err != nil {
		return memory.UnknownValue, err
	}

	pc, ok := runner.program.Entrypoints[funcName]
	if !ok {
		return memory.UnknownValue, fmt.Errorf("unknwon entrypoint: %s", funcName)
	}

	runner.vm.Context.Pc = memory.MemoryAddress{SegmentIndex: VM.ProgramSegment, Offset: pc}
	runner.vm.Context.Ap = offset + 2
	runner.vm.Context.Fp = runner.vm.Context.Ap

	return end, nil
}

func (runner *ZeroRunner) RunUntilPc(pc *memory.MemoryAddress) error {
	for !runner.vm.Context.Pc.Equal(pc) {
		if runner.steps() >= runner.maxsteps {
			return fmt.Errorf(
				"pc %s step %d: max step limit exceeded (%d)",
				runner.pc(),
				runner.steps(),
				runner.maxsteps,
			)
		}

		err := runner.vm.RunStep(nil)
		if err != nil {
			return fmt.Errorf("pc %s step %d: %w", runner.pc(), runner.steps(), err)
		}
	}
	return nil
}

func (runner *ZeroRunner) RunFor(steps uint64) error {
	for runner.steps() < steps {
		if runner.steps() >= runner.maxsteps {
			return fmt.Errorf(
				"pc %s step %d: max step limit exceeded (%d)",
				runner.pc(),
				runner.steps(),
				runner.maxsteps,
			)
		}

		err := runner.vm.RunStep(nil)
		if err != nil {
			return fmt.Errorf(
				"pc %s step %d: %w",
				runner.pc().String(),
				runner.steps(),
				err,
			)
		}
	}
	return nil
}

func (runner *ZeroRunner) BuildProof() ([]byte, []byte, error) {
	relocatedTrace, err := runner.vm.ExecutionTrace()
	if err != nil {
		return nil, nil, err
	}

	return EncodeTrace(relocatedTrace), EncodeMemory(runner.memoryManager.RelocateMemory()), nil
}

func (runner *ZeroRunner) memory() *memory.Memory {
	return runner.memoryManager.Memory
}

func (runner *ZeroRunner) segments() []*memory.Segment {
	return runner.memoryManager.Memory.Segments
}

func (runner *ZeroRunner) pc() memory.MemoryAddress {
	return runner.vm.Context.Pc
}

func (runner *ZeroRunner) steps() uint64 {
	return runner.vm.Step
}

const ctxSize = 3 * 8

func EncodeTrace(trace []vm.Trace) []byte {
	content := make([]byte, 0, len(trace)*ctxSize)
	for i := range trace {
		content = binary.LittleEndian.AppendUint64(content, trace[i].Ap)
		content = binary.LittleEndian.AppendUint64(content, trace[i].Fp)
		content = binary.LittleEndian.AppendUint64(content, trace[i].Pc)
	}
	return content
}

func DecodeTrace(content []byte) []vm.Trace {
	trace := make([]vm.Trace, 0, len(content)/ctxSize)
	for i := 0; i < len(content); i += ctxSize {
		trace = append(
			trace,
			VM.Trace{
				Ap: binary.LittleEndian.Uint64(content[i : i+8]),
				Fp: binary.LittleEndian.Uint64(content[i+8 : i+16]),
				Pc: binary.LittleEndian.Uint64(content[i+16 : i+24]),
			},
		)
	}
	return trace
}

const addrSize = 8
const feltSize = 32

// Encody the relocated memory in the (address, value) form
// in a consecutive way
func EncodeMemory(memory []*f.Element) []byte {
	// Check non nil elements for optimal array size
	nonNilElms := 0
	for i := range memory {
		if memory[i] != nil {
			nonNilElms++
		}
	}
	content := make([]byte, nonNilElms*(addrSize+feltSize))

	count := 0
	for i := range memory {
		if memory[i] == nil {
			continue
		}
		// set the right content index
		j := count * (addrSize + feltSize)
		// store the address
		binary.LittleEndian.PutUint64(content[j:j+addrSize], uint64(i))
		// store the field element
		f.LittleEndian.PutElement(
			(*[32]byte)(content[j+addrSize:j+addrSize+feltSize]),
			*memory[i],
		)

		// increase the number of elements stored
		count++
	}
	return content
}

func DecodeMemory(content []byte) []*f.Element {
	// calculate the max memory index
	lastContentInd := len(content) - (addrSize + feltSize)
	lasMemIndex := binary.LittleEndian.Uint64(content[lastContentInd : lastContentInd+addrSize])

	// create the memory array with the same length as the max memory index
	memory := make([]*f.Element, lasMemIndex+1)

	// decode the encontent and store it in memory
	for i := 0; i < len(content); i += addrSize + feltSize {
		memIndex := binary.LittleEndian.Uint64(content[i : i+addrSize])
		felt, err := f.LittleEndian.Element((*[32]byte)(content[i+addrSize : i+addrSize+feltSize]))
		if err != nil {
			panic(err)
		}
		memory[memIndex] = &felt
	}
	return memory
}
