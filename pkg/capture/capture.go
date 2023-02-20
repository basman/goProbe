/////////////////////////////////////////////////////////////////////////////////
//
// capture.go
//
// Written by Lorenz Breidenbach lob@open.ch, December 2015
// Copyright (c) 2015 Open Systems AG, Switzerland
// All Rights Reserved.
//
/////////////////////////////////////////////////////////////////////////////////

package capture

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/els0r/goProbe/cmd/goProbe/config"
	"github.com/els0r/goProbe/pkg/logging"
	"github.com/els0r/goProbe/pkg/types/hashmap"
	"github.com/fako1024/slimcap/capture"
	"github.com/fako1024/slimcap/capture/afpacket"
)

const (
	// Snaplen sets the amount of bytes captured from a packet
	Snaplen = 128

	// ErrorThreshold is the maximum amount of consecutive errors that can occur on an interface before capturing is halted.
	ErrorThreshold = 10000

	// CaptureTimeout sets the maximum duration pcap waits until polling the kernel for more packets. Our experiments show that you don't want to set this value lower
	// than roughly 100 ms. Otherwise we flood the kernel with syscalls
	// and our performance drops.
	CaptureTimeout time.Duration = 500 * time.Millisecond
)

//////////////////////// Ancillary types ////////////////////////

// State enumerates the activity states of a capture
type State byte

const (
	// StateInitializing means that the capture is setting up
	StateInitializing State = iota + 1
	// StateCapturing means that the capture is actively capturing packets
	StateCapturing
	// StateClose means that the capture is fully terminating and it's held resources are
	// cleaned up
	StateClosing
	// StateError means that the capture has hit the error threshold on the interface (set by ErrorThreshold)
	StateError
)

func (cs State) String() string {
	switch cs {
	case StateInitializing:
		return "StateInitializing"
	case StateCapturing:
		return "StateCapturing"
	case StateClosing:
		return "StateClosing"
	case StateError:
		return "StateError"
	default:
		return "Unknown"
	}
}

// Stats stores the packet statistics of the capture
type Stats struct {
	*CaptureStats
	PacketsLogged int `json:"packets_logged"`
}

// Status stores both the capture's state and statistics
type Status struct {
	State State `json:"state"`
	Stats Stats `json:"stats"`
}

// ErrorMap stores all encountered pcap errors and their number of occurrence
type ErrorMap map[string]int

// String prints the errors that occurred during capturing
func (e ErrorMap) String() string {
	var errs = make([]string, len(e))

	i := 0
	for err, count := range e {
		errs[i] = fmt.Sprintf("%s (%d)", err, count)
		i++
	}
	sort.Slice(errs, func(i, j int) bool {
		return errs[i] < errs[j]
	})
	return strings.Join(errs, "; ")
}

//////////////////////// capture commands ////////////////////////

type command int

const (
	// runtime information
	commandStatus command = iota + 1
	commandErrors
	commandFlows

	// capture state modification
	commandEnable
	commandDisable
	commandRotate
	commandUpdate
	commandClose
)

// captureCommand is an interface implemented by (you guessed it...)
// all capture commands. A capture command is sent to the process() of
// a Capture over the Capture's cmdChan. The captureCommand's execute()
// method is then executed by process() (and in process()'s goroutine).
// As a result we don't have to worry about synchronization of the
// Capture's pcap handle inside the execute() methods.
type captureCommand interface {
	// executes the command on the provided capture instance. It can, but
	// must not provide access to the next state based on its execution
	execute(c *Capture) stateFn
}

// commands for runtime information
type captureCommandStatus struct{ returnChan chan<- Status }
type captureCommandErrors struct{ returnChan chan<- ErrorMap }
type captureCommandFlows struct{ returnChan chan<- *FlowLog }

func (cmd captureCommandStatus) execute(c *Capture) stateFn {
	var result = Status{
		State: c.state,
		Stats: Stats{
			CaptureStats:  c.tryGetCaptureStats(),
			PacketsLogged: c.packetsLogged - c.lastRotationStats.PacketsLogged,
		},
	}
	cmd.returnChan <- result
	return nil
}

func (cmd captureCommandErrors) execute(c *Capture) stateFn {
	cmd.returnChan <- c.errMap
	return nil
}

func (cmd captureCommandFlows) execute(c *Capture) stateFn {
	cmd.returnChan <- c.flowLog
	return nil
}

type captureCommandUpdate struct {
	config config.CaptureConfig
	done   context.CancelFunc
}

func (cmd captureCommandUpdate) execute(c *Capture) stateFn {
	defer cmd.done()

	logger := logging.WithContext(c.ctx)

	if c.needReinitialization(cmd.config) {
		logger.Infof("interface received updated configuration")

		c.reset()
		c.config = cmd.config

		return initializing
	}
	return nil
}

// helper struct to bundle up the multiple return values
// of Rotate
type rotateResult struct {
	agg   *hashmap.Map
	stats Stats
}

type captureCommandRotate struct {
	returnChan chan<- rotateResult
}

func (cmd captureCommandRotate) execute(c *Capture) stateFn {
	logger := logging.WithContext(c.ctx)

	var result rotateResult

	if c.flowLog.Len() == 0 {
		logger.Debug("there are currently no flow records available")
	}

	result.agg = c.flowLog.Rotate()

	stats := c.tryGetCaptureStats()
	lastRotationStats := *stats

	sub(stats, c.lastRotationStats.CaptureStats)

	result.stats = Stats{
		CaptureStats:  stats,
		PacketsLogged: c.packetsLogged - c.lastRotationStats.PacketsLogged,
	}
	c.lastRotationStats = Stats{
		CaptureStats:  &lastRotationStats,
		PacketsLogged: c.packetsLogged,
	}

	cmd.returnChan <- result
	return nil
}

// Capture captures and logs flow data for all traffic on a
// given network interface. For each Capture, a goroutine is
// spawned at creation time. To avoid leaking this goroutine,
// be sure to call Close() when you're done with a Capture.
//
// Each Capture is a finite state machine.

// Each capture is associated with a network interface when created. This interface
// can never be changed.
//
// All public methods of Capture are threadsafe.
type Capture struct {
	iface string
	// synchronizes all access to the Capture's public methods
	mutex sync.Mutex

	// has Close been called on the Capture?
	closed bool

	state State

	config config.CaptureConfig

	// channel over which commands are passed to process()
	// close(cmdChan) is used to tell process() to stop
	cmdChan       chan captureCommand
	captureErrors chan error

	// stats from the last rotation or reset (needed for Status)
	lastRotationStats Stats

	// Counts the total number of logged packets (since the creation of the
	// Capture)
	packetsLogged int

	// Logged flows since creation of the capture (note that some
	// flows are retained even after Rotate has been called)
	flowLog *FlowLog

	// Generic handle / source for packet capture
	captureHandle capture.Source

	// error map for logging errors more properly
	errMap ErrorMap

	// context for cancellation
	ctx context.Context
}

// NewCapture creates a new Capture associated with the given iface.
func NewCapture(ctx context.Context, iface string, config config.CaptureConfig) *Capture {
	// make sure that the interface is set for all log messages using
	// this context
	capCtx := logging.NewContext(ctx, "iface", iface)

	return &Capture{
		iface:         iface,
		mutex:         sync.Mutex{},
		config:        config,
		cmdChan:       make(chan captureCommand),
		captureErrors: make(chan error),
		lastRotationStats: Stats{
			CaptureStats: &CaptureStats{},
		},
		flowLog: NewFlowLog(),
		errMap:  make(map[string]int),
		ctx:     capCtx,
	}
}

// stateFn enables the implementation of the state machine
type stateFn func(*Capture) stateFn

// setState provides write access to the state field of
// a Capture. It also logs the state change.
func (c *Capture) setState(s State) {
	c.state = s
	c.ctx = logging.NewContext(c.ctx, "state", s.String())

	// log state transition
	logger := logging.WithContext(c.ctx)
	logger.Debugf("interface state transition")
}

// Run runs the capture state machine
func (c *Capture) Run() {
	logger := logging.WithContext(c.ctx)

	if c.closed {
		logger.Errorf("unable to run closed capture")
		return
	}

	for state := initializing; state != nil; {
		state = state(c)
	}
}

func initializing(c *Capture) stateFn {
	c.setState(StateInitializing)

	logger := logging.WithContext(c.ctx)
	logger.Info("initializing capture")

	// set up the packet source
	var err error
	c.captureHandle, err = afpacket.NewRingBufSource(c.iface,
		afpacket.CaptureLength(Snaplen),
		afpacket.BufferSize(c.config.BufferSize/4, 4),
		afpacket.Promiscuous(c.config.Promisc),
	)
	if err != nil {
		logger.Errorf("failed to create new packet source: %v", err)
		return inError
	}
	return capturing
}

func capturing(c *Capture) stateFn {
	c.setState(StateCapturing)

	logger := logging.WithContext(c.ctx)
	logger.Info("capturing packets")

	// packet capturing
	go c.process()

	// blocking select to wait for tear down or commands
	select {
	case <-c.ctx.Done():
		return closing
	case cmd := <-c.cmdChan:
		// commands that cause a state transition will provide it
		nextState := cmd.execute(c)
		if nextState != nil {
			return nextState
		}
	case err := <-c.captureErrors:
		logger.Error(err)
		return inError
	}

	// start processing packets
	return nil
}

func inError(c *Capture) stateFn {
	c.setState(StateError)

	logger := logging.WithContext(c.ctx)
	logger.Infof("waiting for configuration update to re-initialize")

	// wait until the capture is closed or an update/re-init command is
	// received
	for {
		select {
		case <-c.ctx.Done():
			return closing
		case cmd := <-c.cmdChan:
			// commands that cause a state transition will provide it
			nextState := cmd.execute(c)
			if nextState != nil {
				return nextState
			}
		}
	}
}

func closing(c *Capture) stateFn {
	c.setState(StateClosing)

	// close the capture and reset fields
	c.reset()

	// make sure no more commands can be received
	close(c.cmdChan)
	c.closed = true

	// exit the state machine
	return nil
}

// reset unites logic used in both recoverError and uninitialize
// in a single method.
func (c *Capture) reset() {
	logger := logging.WithContext(c.ctx)

	if c.captureHandle != nil {
		logger.Infof("closing capture handle")

		err := c.captureHandle.Close()
		if err != nil {
			// for now, just log. We may want to add some additional logic if the close
			// didn't work (which it really shouldn't)
			logger.Error(err)
		}
	}

	// We reset the Pcap part of the stats because we will create
	// a new pcap handle with new counts when the Capture is next
	// initialized. We don't reset the PacketsLogged field because
	// it corresponds to the number of packets in the (untouched)
	// flowLog.
	c.lastRotationStats.CaptureStats = &CaptureStats{}
	c.captureHandle = nil

	// reset the error map. The GC will take care of the previous
	// one
	c.errMap = make(map[string]int)
}

// process is the heart of the Capture. It listens for network traffic on the
// network interface and logs the corresponding flows.
//
// process keeps running until Close is called on its capture handle or it encounters
// a serious capture error
func (c *Capture) process() {
	logger := logging.WithContext(c.ctx)

	var errcount int

	gppacket := GPPacket{}
	pkt := make(afpacket.Packet, Snaplen+6)

	capturePacket := func() (err error) {
		_, err = c.captureHandle.NextPacket(&pkt)
		if err != nil {
			// NextPacket should return a ErrorCaptureClosed in case the handle is closed
			return fmt.Errorf("capture error: %w", err)
		}

		err = gppacket.Populate(&pkt)
		if err == nil {
			c.flowLog.Add(&gppacket)
			errcount = 0
			c.packetsLogged++
		} else {
			errcount++

			// collect the error. The errors value is the key here. Otherwise, the address
			// of the error would be taken, which results in a non-minimal set of errors
			if _, exists := c.errMap[err.Error()]; !exists {
				// TODO: Just logging for now - we might want to construct a new raw data logger that doesn't
				// depend on gopacket (after all we could just dump the raw packet data for later analysis)
				logger.Warnf("discovered faulty packet: %s [%v]", err, pkt.Payload())
			}

			c.errMap[err.Error()]++

			// shut down the interface thread if too many consecutive decoding failures
			// have been encountered
			if errcount > ErrorThreshold {
				return fmt.Errorf("the last %d packets could not be decoded: [%s]",
					ErrorThreshold,
					c.errMap.String(),
				)
			}
		}
		return nil
	}

	// this is the main packet capture loop which an interface should be in most of the time
	for {
		err := capturePacket()
		if err != nil {
			if errors.Is(err, capture.ErrCaptureStopped) { // capture stopped gracefully
				return
			}
			c.captureErrors <- err
			return
		}
	}
}

//////////////////////// utilities ////////////////////////

// needReinitialization checks whether we need to reinitialize the capture
// to apply the given config.
func (c *Capture) needReinitialization(config config.CaptureConfig) bool {
	return c.config != config
}

func (c *Capture) tryGetCaptureStats() *CaptureStats {
	logger := logging.WithContext(c.ctx)

	var (
		stats capture.Stats
		err   error
	)
	if c.captureHandle != nil {
		stats, err = c.captureHandle.Stats()
		if err != nil {
			logger.Errorf("failed to get capture stats: %v", err)
		}
	}
	return &CaptureStats{
		PacketsReceived: stats.PacketsReceived,
		PacketsDropped:  stats.PacketsDropped,
	}
}

//////////////////////// public functions ////////////////////////

// Status returns the current State as well as the statistics
// collected since the last call to Rotate()
//
// Note: If the Capture was reinitialized since the last rotation,
// result.Stats.Pcap will be inaccurate.
//
// Note: result.Stats.Stats may be null if there was an error fetching the
// stats of the underlying pcap handle.
func (c *Capture) Status() (result Status) {
	logger := logging.WithContext(c.ctx)

	c.mutex.Lock()
	defer c.mutex.Unlock()

	if c.closed {
		logger.Errorf("cannot get status of closed capture")
		return
	}

	ch := make(chan Status, 1)
	c.cmdChan <- captureCommandStatus{ch}
	return <-ch
}

// Errors implements the status call to return all interface errors
func (c *Capture) Errors() (result ErrorMap) {
	logger := logging.WithContext(c.ctx)

	c.mutex.Lock()
	defer c.mutex.Unlock()

	if c.closed {
		logger.Errorf("cannot get status of closed capture")
		return
	}

	ch := make(chan ErrorMap, 1)
	c.cmdChan <- captureCommandErrors{ch}
	return <-ch
}

// Flows impolements the status call to return the contents of the active flow log
func (c *Capture) Flows() (result *FlowLog) {
	logger := logging.WithContext(c.ctx)

	c.mutex.Lock()
	defer c.mutex.Unlock()

	if c.closed {
		logger.Errorf("cannot get flows from closed capture")
		return
	}

	ch := make(chan *FlowLog, 1)
	c.cmdChan <- captureCommandFlows{ch}
	return <-ch
}

// Update will attempt to put the Capture instance into
// StateActive with the given config.
// If the Capture is already active with the given config
// Update will detect this and do no work.
func (c *Capture) Update(config config.CaptureConfig) {
	logger := logging.WithContext(c.ctx)

	c.mutex.Lock()
	defer c.mutex.Unlock()

	if c.closed {
		logger.Errorf("cannot get status of closed capture")
		return
	}

	updateCtx, done := context.WithCancel(c.ctx)
	c.cmdChan <- captureCommandUpdate{config, done}

	// wait until the operation completes
	<-updateCtx.Done()
}

// Rotate performs a rotation of the underlying flow log and
// returns an AggFlowMap with all flows that have been collected
// since the last call to Rotate(). It also returns capture statistics
// collected since the last call to Rotate().
//
// Note: stats.Pcap may be null if there was an error fetching the
// stats of the underlying pcap handle.
func (c *Capture) Rotate() (agg *hashmap.AggFlowMap, stats Stats) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	ch := make(chan rotateResult, 1)
	c.cmdChan <- captureCommandRotate{ch}
	result := <-ch
	return result.agg, result.stats
}
