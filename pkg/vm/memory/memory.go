package memory

import (
	"fmt"

	"github.com/NethermindEth/cairo-vm-go/pkg/safemath"
	f "github.com/consensys/gnark-crypto/ecc/stark-curve/fp"
)

type BuiltinRunner interface {
	CheckWrite(segment *Segment, offset uint64, value *MemoryValue) error
	InferValue(segment *Segment, offset uint64) error
}

type NoBuiltin struct{}

func (b *NoBuiltin) CheckWrite(segment *Segment, offset uint64, value *MemoryValue) error {
	return nil
}

func (b *NoBuiltin) InferValue(segment *Segment, offset uint64) error {
	segment.Data[offset] = EmptyMemoryValueAsFelt()
	return nil
}

type Segment struct {
	Data []MemoryValue
	// the max index where a value was written
	LastIndex     int
	BuiltinRunner BuiltinRunner
}

func (segment *Segment) WithBuiltinRunner(builtinRunner BuiltinRunner) *Segment {
	segment.BuiltinRunner = builtinRunner
	return segment
}

func EmptySegment() *Segment {
	// empty segments have capacity 100 as a default
	return &Segment{
		Data:          make([]MemoryValue, 0, 100),
		LastIndex:     -1,
		BuiltinRunner: &NoBuiltin{},
	}
}

func EmptySegmentWithCapacity(capacity int) *Segment {
	return &Segment{
		Data:          make([]MemoryValue, 0, capacity),
		LastIndex:     -1,
		BuiltinRunner: &NoBuiltin{},
	}
}

func EmptySegmentWithLength(length int) *Segment {
	return &Segment{
		Data:          make([]MemoryValue, length),
		LastIndex:     length - 1,
		BuiltinRunner: &NoBuiltin{},
	}
}

// returns the effective size of a segment length
// i.e the rightmost element index + 1
func (segment *Segment) Len() uint64 {
	return uint64(segment.LastIndex + 1)
}

// returns the real length that a segmen has
func (segment *Segment) RealLen() uint64 {
	return uint64(len(segment.Data))
}

// Writes a new memory value to a specified offset, errors in case of overwriting an existing cell
func (segment *Segment) Write(offset uint64, value *MemoryValue) error {
	if offset >= segment.RealLen() {
		segment.IncreaseSegmentSize(offset + 1)
	}
	if offset >= segment.Len() {
		segment.LastIndex = int(offset)
	}

	cell := &segment.Data[offset]
	if cell.Known() && !cell.Equal(value) {
		return fmt.Errorf(
			"rewriting cell: old value: %s, new value: %s",
			cell.String(),
			value.String(),
		)
	}
	segment.Data[offset] = *value
	return segment.BuiltinRunner.CheckWrite(segment, offset, value)
}

// Reads a memory value from a specified offset at the segment
func (segment *Segment) Read(offset uint64) (MemoryValue, error) {
	if offset >= segment.RealLen() {
		segment.IncreaseSegmentSize(offset + 1)
	}
	if offset > segment.Len() {
		segment.LastIndex = int(offset)
	}

	cell := &segment.Data[offset]
	if !cell.Known() {
		if err := segment.BuiltinRunner.InferValue(segment, offset); err != nil {
			return MemoryValue{}, err
		}
	}
	return *cell, nil
}

func (segment *Segment) Peek(offset uint64) MemoryValue {
	if offset >= segment.RealLen() {
		segment.IncreaseSegmentSize(offset + 1)
	}
	if offset >= segment.Len() {
		segment.LastIndex = int(offset)
	}
	return segment.Data[offset]
}

// Increase a segment allocated space. Panics if the new size is smaller
func (segment *Segment) IncreaseSegmentSize(newSize uint64) {
	segmentData := segment.Data
	if len(segmentData) > int(newSize) {
		panic(fmt.Sprintf(
			"cannot decrease segment size: %d -> %d",
			len(segmentData),
			newSize,
		))
	}

	var newSegmentData []MemoryValue
	if cap(segmentData) > int(newSize) {
		newSegmentData = segmentData[:cap(segmentData)]
	} else {
		newSegmentData = make([]MemoryValue, safemath.Max(newSize, uint64(len(segmentData)*2)))
		copy(newSegmentData, segmentData)
	}
	segment.Data = newSegmentData
}

//func (segment *Segment) String() string {
//	repr := make([]string, len(segment.Data))
//	for i, cell := range segment.Data {
//		if i < len(segment.Data)-5 {
//			continue
//		}
//		if cell.Accessed {
//			repr[i] = cell.Value.String()
//		} else {
//			repr[i] = "-"
//		}
//	}
//	return strings.Join(repr, ", ")
//}

func (segment *Segment) String() string {
	header := fmt.Sprintf(
		"real len: %d real cap: %d len: %d\n",
		len(segment.Data),
		cap(segment.Data),
		segment.Len(),
	)
	for i := range segment.Data {
		if i < int(segment.Len())-5 {
			continue
		}
		if segment.Data[i].Known() {
			header += fmt.Sprintf("[%d]-> %s\n", i, segment.Data[i].String())
		}
	}
	return header
}

// todo(rodro): Check out temprary segments
// Represents the whole VM memory divided into segments
type Memory struct {
	Segments []*Segment
}

// todo(rodro): can the amount of segments be known before hand?
func InitializeEmptyMemory() *Memory {
	return &Memory{
		// capacity 4 should be enough for the minimum amount of segments
		Segments: make([]*Segment, 0, 4),
	}
}

// Allocates a new segment providing its initial data and returns its index
func (memory *Memory) AllocateSegment(data []*f.Element) (int, error) {
	newSegment := EmptySegmentWithLength(len(data))
	for i := range data {
		memVal := MemoryValueFromFieldElement(data[i])
		err := newSegment.Write(uint64(i), &memVal)
		if err != nil {
			return 0, err
		}
	}
	memory.Segments = append(memory.Segments, newSegment)
	return len(memory.Segments) - 1, nil
}

// Allocates an empty segment and returns its index
func (memory *Memory) AllocateEmptySegment() int {
	memory.Segments = append(memory.Segments, EmptySegment())
	return len(memory.Segments) - 1
}

// Writes to a memory address a new memory value. Errors if writing to an unallocated
// space or if rewriting a specific cell
func (memory *Memory) Write(segmentIndex uint64, offset uint64, value *MemoryValue) error {
	if segmentIndex >= uint64(len(memory.Segments)) {
		return fmt.Errorf("unallocated segment at index %d", segmentIndex)
	}
	return memory.Segments[segmentIndex].Write(offset, value)
}

func (memory *Memory) WriteToAddress(address *MemoryAddress, value *MemoryValue) error {
	return memory.Write(address.SegmentIndex, address.Offset, value)
}

// Reads a memory value given the segment index and offset. Errors if reading from
// an unallocated space. If reading a cell which hasn't been accesed before, it is
// initalized with its default zero value
func (memory *Memory) Read(segmentIndex uint64, offset uint64) (MemoryValue, error) {
	if segmentIndex >= uint64(len(memory.Segments)) {
		return MemoryValue{}, fmt.Errorf("unallocated segment at index %d", segmentIndex)
	}
	return memory.Segments[segmentIndex].Read(offset)
}

// Reads a memory value from a memory address. Errors if reading from an unallocated
// space. If reading a cell which hasn't been accesed before, it is initalized with
// its default zero value
func (memory *Memory) ReadFromAddress(address *MemoryAddress) (MemoryValue, error) {
	return memory.Read(address.SegmentIndex, address.Offset)
}

// Given a segment index and offset returns a pointer to the Memory Cell
func (memory *Memory) Peek(segmentIndex uint64, offset uint64) (MemoryValue, error) {
	if segmentIndex >= uint64(len(memory.Segments)) {
		return MemoryValue{}, fmt.Errorf("unallocated segment at index %d", segmentIndex)
	}
	return memory.Segments[segmentIndex].Peek(offset), nil
}

// Given a Memory Address returns a pointer to the Memory Cell
func (memory *Memory) PeekFromAddress(address *MemoryAddress) (MemoryValue, error) {
	return memory.Peek(address.SegmentIndex, address.Offset)
}
