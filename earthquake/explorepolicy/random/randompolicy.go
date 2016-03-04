// Copyright (C) 2015 Nippon Telegraph and Telephone Corporation.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or
// implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package random provides the default non-deterministic policy
package random

import (
	"fmt"
	log "github.com/cihub/seelog"
	"github.com/osrg/earthquake/earthquake/historystorage"
	"github.com/osrg/earthquake/earthquake/signal"
	"github.com/osrg/earthquake/earthquake/util/config"
	queue "github.com/osrg/earthquake/earthquake/util/queue"
	"math/rand"
	"time"
)

type Random struct {
	// channel
	nextActionChan chan signal.Action

	// queue
	queue      queue.TimeBoundedQueue
	queueDeqCh chan queue.TimeBoundedQueueItem

	// shell action routine
	shelActionRoutineRunning bool

	// parameter "minInterval"
	MinInterval time.Duration

	// parameter "maxInterval"
	MaxInterval time.Duration

	// parameter "prioritizedEntities"
	PrioritizedEntities map[string]bool

	// parameter "shellActionInterval"
	ShellActionInterval time.Duration

	// parameter "shellActionCommand"
	ShellActionCommand string

	// parameter "faultActionProbability”
	FaultActionProbability float64

	// parameter "procResetSchedProbability”
	ProcResetSchedProbability float64
}

func New() *Random {
	nextActionChan := make(chan signal.Action)
	q := queue.NewBasicTBQueue()
	r := &Random{
		nextActionChan:            nextActionChan,
		queue:                     q,
		queueDeqCh:                q.GetDequeueChan(),
		shelActionRoutineRunning:  false,
		MinInterval:               time.Duration(0),
		MaxInterval:               time.Duration(0),
		PrioritizedEntities:       make(map[string]bool, 0),
		ShellActionInterval:       time.Duration(0),
		ShellActionCommand:        "",
		FaultActionProbability:    0.0,
		ProcResetSchedProbability: 0.1,
	}
	go r.dequeueEventRoutine()
	return r
}

const Name = "random"

// returns "random"
func (this *Random) Name() string {
	return Name
}

// parameters:
//  - minInterval(duration): min interval in millisecs (default: 0 msecs)
//
//  - maxInterval(duration): max interval (default == minInterval)
//
//  - prioritizedEntities([]string): prioritized entity string (default: empty)
//
//  - shellActionInterval(duration): interval in millisecs for injecting ShellAction (default: 0)
//    NOTE: this can be 0 only if shellFaultCommand=""(empty string))
//
//  - shellActionCommand(string): command string for injecting ShellAction (default: empty string "")
//    NOTE: the command execution blocks.
//
//  - faultActionProbability(float64): probability (0.0-1.0) of PacketFaultAction/FilesystemFaultAction (default: 0.0)
//
//  - procResetSchedProbability(float64): probability (0.0-1.0) for resetting ProcSetSchedAction (default: 0.1)
//
// should support dynamic reloading
func (r *Random) LoadConfig(cfg config.Config) error {
	policyName := cfg.GetString("explorePolicy")
	if policyName != r.Name() {
		log.Warnf("Policy name mismatch: \"%s\" != \"%s\"", policyName, r.Name())
	}

	epp := "explorepolicyparam."
	paramMinInterval := epp + "minInterval"
	if cfg.IsSet(paramMinInterval) {
		r.MinInterval = cfg.GetDuration(paramMinInterval)
		log.Infof("Set minInterval=%s", r.MinInterval)
	} else {
		log.Infof("Using default minInterval=%s", r.MinInterval)
	}

	paramMaxInterval := epp + "maxInterval"
	if cfg.IsSet(paramMaxInterval) {
		r.MaxInterval = cfg.GetDuration(paramMaxInterval)
		log.Infof("Set maxInterval=%s", r.MaxInterval)
	} else {
		// set non-zero default value
		r.MaxInterval = r.MinInterval
		log.Infof("Using default maxInterval=%s", r.MaxInterval)
	}

	paramPrioritizedEntities := epp + "prioritizedEntities"
	if cfg.IsSet(paramPrioritizedEntities) {
		slice := cfg.GetStringSlice(paramPrioritizedEntities)
		if slice != nil {
			for i := 0; i < len(slice); i++ {
				r.PrioritizedEntities[slice[i]] = true
			}
			log.Debugf("Set prioritizedEntities=%s", r.PrioritizedEntities)
		}
	}

	paramShellActionInterval := epp + "shellActionInterval"
	if cfg.IsSet(paramShellActionInterval) {
		r.ShellActionInterval = cfg.GetDuration(paramShellActionInterval)
		log.Infof("Set shellActionInterval=%s", r.ShellActionInterval)
	}

	paramShellActionCommand := epp + "shellActionCommand"
	if cfg.IsSet(paramShellActionCommand) {
		r.ShellActionCommand = cfg.GetString(paramShellActionCommand)
		log.Infof("Set shellActionCommand=%s", r.ShellActionCommand)
	}

	if r.ShellActionInterval < 0 {
		return fmt.Errorf("shellActionInterval(=%s) must be non-negative value", r.ShellActionInterval)
	}

	if r.ShellActionInterval == 0 && r.ShellActionCommand != "" {
		log.Warn("shellActionCommand will be ignored, because shellActionInterval is zero.")
	}

	if r.ShellActionInterval > 0 && !r.shelActionRoutineRunning {
		// FIXME: not thread safe!
		r.shelActionRoutineRunning = true
		go r.shellFaultInjectionRoutine()
	}

	paramFaultActionProbability := epp + "faultActionProbability"
	if cfg.IsSet(paramFaultActionProbability) {
		r.FaultActionProbability = cfg.GetFloat64(paramFaultActionProbability)
		log.Infof("Set faultActionProbability=%f", r.FaultActionProbability)
	}
	if r.FaultActionProbability < 0.0 || r.FaultActionProbability > 1.0 {
		return fmt.Errorf("bad faultActionProbability %f", r.FaultActionProbability)
	}

	paramProcResetSchedProbability := epp + "procResetSchedProbability"
	if cfg.IsSet(paramProcResetSchedProbability) {
		r.ProcResetSchedProbability = cfg.GetFloat64(paramProcResetSchedProbability)
		log.Infof("Set procResetSchedProbability=%f", r.ProcResetSchedProbability)
	}
	if r.ProcResetSchedProbability < 0.0 || r.ProcResetSchedProbability > 1.0 {
		return fmt.Errorf("bad procResetSchedProbability %f", r.ProcResetSchedProbability)
	}
	return nil
}

func (r *Random) SetHistoryStorage(storage historystorage.HistoryStorage) error {
	return nil
}

func (r *Random) ActionChan() chan signal.Action {
	return r.nextActionChan
}

// put a ShellAction to nextActionChan
func (r *Random) shellFaultInjectionRoutine() {
	if r.ShellActionInterval == 0 {
		panic(fmt.Errorf("implementation error. should not be called here."))
	}
	for {
		<-time.After(r.ShellActionInterval)
		// NOTE: you can also set arbitrary info (e.g., expected shutdown or unexpected kill)
		comments := map[string]interface{}{
			"comment": "injected by the random explorer",
		}
		action, err := signal.NewShellAction(r.ShellActionCommand, comments)
		if err != nil {
			panic(log.Critical(err))
		}
		r.nextActionChan <- action
	}
}

// for dequeueRoutine()
func (r *Random) makeActionForEvent(event signal.Event) (signal.Action, error) {
	switch event.(type) {
	case *signal.ProcSetEvent:
		return r.makeActionForProcSetEvent(event.(*signal.ProcSetEvent))
	}
	defaultAction, defaultActionErr := event.DefaultAction()
	faultAction, faultActionErr := event.DefaultFaultAction()
	if faultAction == nil {
		return defaultAction, defaultActionErr
	}
	if rand.Intn(999) < int(r.FaultActionProbability*1000.0) {
		log.Debugf("Injecting fault %s for %s", faultAction, event)
		return faultAction, faultActionErr
	} else {
		return defaultAction, defaultActionErr
	}
}

// dequeue event, determine corresponding action, and put the action to nextActionChan
func (r *Random) dequeueEventRoutine() {
	for {
		qItem := <-r.queueDeqCh
		event := qItem.Value().(signal.Event)
		action, err := r.makeActionForEvent(event)
		log.Debugf("RANDOM: Determined action %#v for event %#v", action, event)
		if err != nil {
			panic(log.Critical(err))
		}
		r.nextActionChan <- action
	}
}

func (r *Random) QueueEvent(event signal.Event) {
	minInterval := r.MinInterval
	maxInterval := r.MaxInterval
	_, prioritized := r.PrioritizedEntities[event.EntityID()]
	if prioritized {
		// FIXME: magic coefficient for prioritizing (decrease intervals)
		minInterval = time.Duration(float64(minInterval) * 0.8)
		maxInterval = time.Duration(float64(maxInterval) * 0.8)
	}
	item, err := queue.NewBasicTBQueueItem(event, minInterval, maxInterval)
	if err != nil {
		panic(log.Critical(err))
	}
	r.queue.Enqueue(item)
}
