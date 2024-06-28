//go:build rp2040
// +build rp2040

package machine

import (
	"device/rp"
	"runtime/volatile"
	"unsafe"
)

var (
	PIO0 = &PIO{
		Device: rp.PIO0,
	}
	PIO1 = &PIO{
		Device: rp.PIO1,
	}
)

const (
	REG_ALIAS_RW_BITS  = 0x0 << 12
	REG_ALIAS_XOR_BITS = 0x1 << 12
	REG_ALIAS_SET_BITS = 0x2 << 12
	REG_ALIAS_CLR_BITS = 0x3 << 12
)

// PIO represents one of the two PIO peripherals in the RP2040
type PIO struct {
	// Bitmask of used instruction space
	usedSpaceMask uint32

	// Index of this PIO (0 or 1)
	Index uint8

	// StateMachines provides access to the 4 state machines in this PIO
	StateMachines [4]PIOStateMachine

	// Device is the actual hardware device
	Device *rp.PIO0_Type
}

type PIOStateMachineReg uint8

const (
	PIOStateMachineClkDivReg    PIOStateMachineReg = 0
	PIOStateMachineExecCtrlReg                     = 4
	PIOStateMachineShiftCtrlReg                    = 8
	PIOStateMachineAddrReg                         = 12
	PIOStateMachineInstrReg                        = 16
	PIOStateMachinePinCtrlReg                      = 20
)

// PIOStateMachine represents one of the four state machines in a PIO
type PIOStateMachine struct {
	// The PIO containing this state machine
	PIO *PIO

	// Index of this state machine 0-3
	Index uint8

	// Cached pointers to the sm TX and Rx registers
	// Saves working out their location for every Put() or Get()
	TxReg *volatile.Register32
	RxReg *volatile.Register32
}

// PIOStateMachineConfig holds the configuration for a PIO state
// machine.
//
// This type is used by code generated by pioasm, in the RP2040
// c-sdk - any changes should be backwards compatible.
type PIOStateMachineConfig struct {
	ClkDiv    uint32
	ExecCtrl  uint32
	ShiftCtrl uint32
	PinCtrl   uint32
}

// PIOProgram holds the assembled PIO code
//
// This type is used by code generated by pioasm, in the RP2040
// c-sdk - any changes should be backwards compatible.
type PIOProgram struct {
	// Instructions holds the binary code in 16-bit words
	Instructions []uint16

	// Origin indicates where in the PIO execution memory
	// the program must be loaded, or -1 if the code is
	// position independant
	Origin int8
}

func (pio *PIO) Configure() {
	pio.Index = 0
	if pio == PIO1 {
		pio.Index = 1
	}

	for i := 0; i < 4; i++ {
		pio.StateMachines[i].PIO = pio
		pio.StateMachines[i].Index = uint8(i)
	}
}

// AddProgram loads a PIO program into PIO memory
func (pio *PIO) AddProgram(program *PIOProgram) uint8 {
	offset := pio.findOffsetForProgram(program)
	if offset < 0 {
		panic("no program space")
	}
	pio.AddProgramAtOffset(program, uint8(offset))
	return uint8(offset)
}

func (pio *PIO) AddProgramAtOffset(program *PIOProgram, offset uint8) {
	if !pio.CanAddProgramAtOffset(program, offset) {
		panic("no program space")
	}

	programLen := uint8(len(program.Instructions))
	for i := uint8(0); i < programLen; i++ {
		instr := program.Instructions[i]

		// Patch jump instructions with relative offset
		if PIO_INSTR_BITS_JMP == instr&PIO_INSTR_BITS_Msk {
			pio.writeInstructionMemory(offset+i, instr+uint16(offset))
		} else {
			pio.writeInstructionMemory(offset+i, instr)
		}
	}

	// Mark the instruction space as in-use
	programMask := uint32((1 << programLen) - 1)
	pio.usedSpaceMask |= programMask << uint32(offset)
}

func (pio *PIO) CanAddProgramAtOffset(program *PIOProgram, offset uint8) bool {
	// Non-relocatable programs must be added at offset
	if program.Origin >= 0 && program.Origin != int8(offset) {
		return false
	}

	programMask := uint32((1 << len(program.Instructions)) - 1)
	return pio.usedSpaceMask&(programMask<<offset) == 0
}

func (pio *PIO) writeInstructionMemory(offset uint8, value uint16) {
	// Instead of using MEM0, MEM1, etc, calculate the offset of the
	// disired register starting at MEM0
	start := unsafe.Pointer(&pio.Device.INSTR_MEM0)

	// Instruction Memory registers are 32-bit, with only lower 16 used
	reg := (*volatile.Register32)(unsafe.Pointer(uintptr(start) + uintptr(offset)*4))
	reg.Set(uint32(value))
}

func (pio *PIO) findOffsetForProgram(program *PIOProgram) int8 {
	programLen := uint32(len(program.Instructions))
	programMask := uint32((1 << programLen) - 1)

	// Program has fixed offset (not relocatable)
	if program.Origin >= 0 {
		if uint32(program.Origin) > 32-programLen {
			return -1
		}

		if (pio.usedSpaceMask & (programMask << program.Origin)) != 0 {
			return -1
		}

		return program.Origin
	}

	// work down from the top always
	for i := int8(32 - programLen); i >= 0; i-- {
		if pio.usedSpaceMask&(programMask<<uint32(i)) == 0 {
			return i
		}
	}

	return -1
}

// DefaultStateMachineConfig returns the default configuration
// for a PIO state machine.
//
// The default configuration here, mirrors the state from
// pio_get_default_sm_config in the c-sdk.
//
// This function is used by code generated by pioasm, in the RP2040
// c-sdk - any changes should be backwards compatible.
func DefaultStateMachineConfig() PIOStateMachineConfig {
	cfg := PIOStateMachineConfig{}
	cfg.SetClkDivIntFrac(1, 0)
	cfg.SetWrap(0, 31)
	cfg.SetInShift(true, false, 32)
	cfg.SetOutShift(true, false, 32)
	return cfg
}

// SetClkDivIntFrac sets the clock divider for the state
// machine from a whole and fractional part.
func (cfg *PIOStateMachineConfig) SetClkDivIntFrac(div uint16, frac uint8) {
	cfg.ClkDiv = (uint32(frac) << rp.PIO0_SM0_CLKDIV_FRAC_Pos) |
		(uint32(div) << rp.PIO0_SM0_CLKDIV_INT_Pos)
}

// SetClkHz sets the state machine clock frequency in Hz
//
// hz must be between sysClk/0xFFFF and sysClk
// Or 1,907 and 125,000,000 when sys clk is the default 125 MHz
func (cfg *PIOStateMachineConfig) SetClkHz(hz uint32) {
	d := uint16(CPUFrequency() / hz)
	f := uint8(((CPUFrequency() % hz) << 8) / hz)
	cfg.SetClkDivIntFrac(d, f)
}

// SetWrap sets the wrapping configuration for the state machine
//
// This function is used by code generated by pioasm, in the RP2040
// c-sdk - any changes should be backwards compatible.
func (cfg *PIOStateMachineConfig) SetWrap(wrapTarget uint8, wrap uint8) {
	cfg.ExecCtrl =
		(cfg.ExecCtrl & ^uint32(rp.PIO0_SM0_EXECCTRL_WRAP_TOP_Msk|rp.PIO0_SM0_EXECCTRL_WRAP_BOTTOM_Msk)) |
			(uint32(wrapTarget) << rp.PIO0_SM0_EXECCTRL_WRAP_BOTTOM_Pos) |
			(uint32(wrap) << rp.PIO0_SM0_EXECCTRL_WRAP_TOP_Pos)
}

// SetInShift sets the 'in' shifting parameters in a state machine configuration
func (cfg *PIOStateMachineConfig) SetInShift(shiftRight bool, autoPush bool, pushThreshold uint16) {
	cfg.ShiftCtrl = cfg.ShiftCtrl &
		^uint32(rp.PIO0_SM0_SHIFTCTRL_IN_SHIFTDIR_Msk|
			rp.PIO0_SM0_SHIFTCTRL_AUTOPUSH_Msk|
			rp.PIO0_SM0_SHIFTCTRL_PUSH_THRESH_Msk) |
		(boolToBit(shiftRight) << rp.PIO0_SM0_SHIFTCTRL_IN_SHIFTDIR_Pos) |
		(boolToBit(autoPush) << rp.PIO0_SM0_SHIFTCTRL_AUTOPUSH_Pos) |
		(uint32(pushThreshold&0x1f) << rp.PIO0_SM0_SHIFTCTRL_PUSH_THRESH_Pos)
}

// SetOutShift sets the 'out' shifting parameters in a state machine configuration
func (cfg *PIOStateMachineConfig) SetOutShift(shiftRight bool, autoPull bool, pullThreshold uint16) {
	cfg.ShiftCtrl = cfg.ShiftCtrl &
		^uint32(rp.PIO0_SM0_SHIFTCTRL_OUT_SHIFTDIR_Msk|
			rp.PIO0_SM0_SHIFTCTRL_AUTOPULL_Msk|
			rp.PIO0_SM0_SHIFTCTRL_PULL_THRESH_Msk) |
		(boolToBit(shiftRight) << rp.PIO0_SM0_SHIFTCTRL_OUT_SHIFTDIR_Pos) |
		(boolToBit(autoPull) << rp.PIO0_SM0_SHIFTCTRL_AUTOPULL_Pos) |
		(uint32(pullThreshold&0x1f) << rp.PIO0_SM0_SHIFTCTRL_PULL_THRESH_Pos)
}

// SetSideSet sets the sideset parameters in a state machine configuration
//
// This function is used by code generated by pioasm, in the RP2040
// c-sdk - any changes should be backwards compatible.
func (cfg *PIOStateMachineConfig) SetSideSet(bitCount uint8, optional bool, pindirs bool) {
	cfg.PinCtrl = (cfg.PinCtrl & ^uint32(rp.PIO0_SM0_PINCTRL_SIDESET_COUNT_Msk)) |
		(uint32(bitCount) << uint32(rp.PIO0_SM0_PINCTRL_SIDESET_COUNT_Pos))

	cfg.ExecCtrl = (cfg.ExecCtrl & ^uint32(rp.PIO0_SM0_EXECCTRL_SIDE_EN_Msk|rp.PIO0_SM0_EXECCTRL_SIDE_PINDIR_Msk)) |
		(boolToBit(optional) << rp.PIO0_SM0_EXECCTRL_SIDE_EN_Pos) |
		(boolToBit(pindirs) << rp.PIO0_SM0_EXECCTRL_SIDE_PINDIR_Pos)
}

// SetOutPins sets the base Out pin and count of consecutive Out pins
//
// outBase 0-31 First pin to set as output
// outCount 0-32 Number of pins to set
func (cfg *PIOStateMachineConfig) SetOutPins(outBase Pin, outCount uint8) {
	cfg.PinCtrl = (cfg.PinCtrl & ^uint32(rp.PIO0_SM0_PINCTRL_OUT_BASE_Msk|rp.PIO0_SM0_PINCTRL_OUT_COUNT_Msk)) |
		(uint32(outBase) << rp.PIO0_SM0_PINCTRL_OUT_BASE_Pos) |
		(uint32(outCount) << rp.PIO0_SM0_PINCTRL_OUT_COUNT_Pos)
}

// SetSetPins sets the base Set pin and count of consecutive Set pins
//
// The PIO 'set' instruction modifies these pins
func (cfg *PIOStateMachineConfig) SetSetPins(base Pin, count uint8) {
	cfg.PinCtrl = (cfg.PinCtrl & ^uint32(rp.PIO0_SM0_PINCTRL_SET_BASE_Msk|rp.PIO0_SM0_PINCTRL_SET_COUNT_Msk)) |
		(uint32(base) << rp.PIO0_SM0_PINCTRL_SET_BASE_Pos) |
		(uint32(count) << rp.PIO0_SM0_PINCTRL_SET_COUNT_Pos)
}

// SetInBase sets the base In pin
//
// inBase 0-31 First pin to use as input
func (cfg *PIOStateMachineConfig) SetInBase(inBase Pin) {
	cfg.PinCtrl = (cfg.PinCtrl & ^uint32(rp.PIO0_SM0_PINCTRL_IN_BASE_Msk)) |
		(uint32(inBase) << rp.PIO0_SM0_PINCTRL_IN_BASE_Pos)
}

// SetSideSetBase sets the base Side Set pin
//
// sideSetBase 0-31 First pin to use as output
func (cfg *PIOStateMachineConfig) SetSideSetBase(sideSetBase Pin) {
	cfg.PinCtrl = (cfg.PinCtrl & ^uint32(rp.PIO0_SM0_PINCTRL_SIDESET_BASE_Msk)) |
		(uint32(sideSetBase) << rp.PIO0_SM0_PINCTRL_SIDESET_BASE_Pos)
}

// Init initializes the state machine
//
// initialPC is the initial program counter
// cfg is optional.  If not specified the default config will be used
func (sm *PIOStateMachine) Init(initialPC uint8, cfg *PIOStateMachineConfig) {
	// Halt the state machine to set sensible defaults
	sm.SetEnabled(false)

	if cfg == nil {
		cfg := DefaultStateMachineConfig()
		sm.SetConfig(&cfg)
	} else {
		sm.SetConfig(cfg)
	}

	sm.ClearFIFOs()

	// Cache pointers to sm TX & RX regs.
	// Avoids locating them for every Put() and Get()
	start := unsafe.Pointer(&sm.PIO.Device.TXF0)
	// 4 bytes (1 register) per state machine
	offset := uintptr(sm.Index) * 4
	sm.TxReg = (*volatile.Register32)(unsafe.Pointer(uintptr(start) + offset))
	start = unsafe.Pointer(&sm.PIO.Device.RXF0)
	sm.RxReg = (*volatile.Register32)(unsafe.Pointer(uintptr(start) + offset))

	// Clear FIFO debug flags
	fdebugMask := uint32((1 << rp.PIO0_FDEBUG_TXOVER_Pos) |
		(1 << rp.PIO0_FDEBUG_RXUNDER_Pos) |
		(1 << rp.PIO0_FDEBUG_TXSTALL_Pos) |
		(1 << rp.PIO0_FDEBUG_RXSTALL_Pos))
	sm.PIO.Device.FDEBUG.Set(fdebugMask << sm.Index)

	sm.Restart()
	sm.ClkDivRestart()
	sm.Exec(PIOEncodeJmp(uint16(initialPC)))
}

// SetEnabled controls whether the state machine is running
func (sm *PIOStateMachine) SetEnabled(enabled bool) {
	sm.PIO.Device.CTRL.ReplaceBits(boolToBit(enabled), 0x1, sm.Index)
}

// Restart restarts the state machine
func (sm *PIOStateMachine) Restart() {
	sm.PIO.Device.CTRL.SetBits(1 << (rp.PIO0_CTRL_SM_RESTART_Pos + sm.Index))
}

// Restart a state machine clock divider with a phase of 0
func (sm *PIOStateMachine) ClkDivRestart() {
	sm.PIO.Device.CTRL.SetBits(1 << (rp.PIO0_CTRL_CLKDIV_RESTART_Pos + sm.Index))
}

// SetConfig applies state machine configuration to a state machine
func (sm *PIOStateMachine) SetConfig(cfg *PIOStateMachineConfig) {
	sm.GetRegister(PIOStateMachineClkDivReg).Set(cfg.ClkDiv)
	sm.GetRegister(PIOStateMachineExecCtrlReg).Set(cfg.ExecCtrl)
	sm.GetRegister(PIOStateMachineShiftCtrlReg).Set(cfg.ShiftCtrl)
	sm.GetRegister(PIOStateMachinePinCtrlReg).Set(cfg.PinCtrl)
}

// Tx and Rx are aliases for the Put & Get methods
var Tx = (*PIOStateMachine).Put
var Rx = (*PIOStateMachine).Get

// Put writes a word of data to a state machine's Tx FIFO
//
// Blocks while Tx FIFO is full
func (sm *PIOStateMachine) Put(data uint32) {
	for sm.PIO.Device.FSTAT.HasBits(1 << (rp.PIO0_FSTAT_TXFULL_Pos + sm.Index)) {
	}
	sm.TxReg.Set(data)
}

// Get reads a word of data from state machine's Rx FIFO
//
// Blocks while Rx FIFO is empty
func (sm *PIOStateMachine) Get() (data uint32) {
	for sm.PIO.Device.FSTAT.HasBits(1 << (rp.PIO0_FSTAT_RXEMPTY_Pos + sm.Index)) {
	}
	return sm.RxReg.Get()
}

// GetRegister gets a pointer to the indicated register of a state machine
//
// This method abstracts the layout of the state machines in the PIO register
// space from the caller.
func (sm *PIOStateMachine) GetRegister(reg PIOStateMachineReg) *volatile.Register32 {
	// SM0_CLKDIV is the first register of the first state machine
	start := unsafe.Pointer(&sm.PIO.Device.SM0_CLKDIV)

	// 24 bytes (6 registers) per state machine
	offset := uintptr(sm.Index) * 24

	// reg encodes the register offset within the state machine
	offset += uintptr(reg)

	return (*volatile.Register32)(unsafe.Pointer(uintptr(start) + offset))
}

// SetConsecurityPinDirs sets a range of pins to either 'in' or 'out'
func (sm *PIOStateMachine) SetConsecutivePinDirs(pin Pin, count uint8, isOut bool) {
	reg := sm.GetRegister(PIOStateMachinePinCtrlReg)

	pinctrl_saved := reg.Get()
	pindir_val := uint16(0)
	if isOut {
		pindir_val = 0x1f
	}

	for count > 5 {
		reg.Set((5 << rp.PIO0_SM0_PINCTRL_SET_COUNT_Pos) | (uint32(pin) << rp.PIO0_SM0_PINCTRL_SET_BASE_Pos))
		sm.Exec(PIOEncodeSet(PIOSrcDestPinDirs, pindir_val))
		count -= 5
		pin = (pin + 5) & 0x1f
	}

	reg.Set((uint32(count) << rp.PIO0_SM0_PINCTRL_SET_COUNT_Pos) | (uint32(pin) << rp.PIO0_SM0_PINCTRL_SET_BASE_Pos))
	sm.Exec(PIOEncodeSet(PIOSrcDestPinDirs, pindir_val))
	reg.Set(pinctrl_saved)
}

// Exec will immediately execute an instruction on the state machine
func (sm *PIOStateMachine) Exec(instr uint16) {
	reg := sm.GetRegister(PIOStateMachineInstrReg)
	reg.Set(uint32(instr))
}

func (sm *PIOStateMachine) ClearFIFOs() {
	xorReg := XORRegister(sm.GetRegister(PIOStateMachineShiftCtrlReg))

	xorReg.Set(rp.PIO0_SM0_SHIFTCTRL_FJOIN_RX_Msk)
	xorReg.Set(rp.PIO0_SM0_SHIFTCTRL_FJOIN_RX_Msk)
}

// XORRegiser returns the 'XOR' alias for a register
//
// Registers have 'ALIAS' registers with special semantics, see
// 2.1.2. Atomic Register Access in the RP2040 Datasheet
//
// Each peripheral register block is allocated 4kB of address space, with registers accessed using one of 4 methods,
// selected by address decode.
//   - Addr + 0x0000 : normal read write access
//   - Addr + 0x1000 : atomic XOR on write
//   - Addr + 0x2000 : atomic bitmask set on write
//   - Addr + 0x3000 : atomic bitmask clear on write
func XORRegister(reg *volatile.Register32) *volatile.Register32 {
	return (*volatile.Register32)(unsafe.Pointer(uintptr(unsafe.Pointer(reg)) | REG_ALIAS_XOR_BITS))
}
