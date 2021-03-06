package resource

import (
	// "errors"
	"fmt"
	"github.com/boltdb/bolt"
	"github.com/golang/glog"
	"github.com/open-horizon/anax/config"
	"github.com/open-horizon/anax/events"
	"github.com/open-horizon/anax/persistence"
	"github.com/open-horizon/anax/worker"
)

type ResourceWorker struct {
	worker.BaseWorker // embedded field
	db                *bolt.DB
	rm                *ResourceManager
	am                *AuthenticationManager
}

func NewResourceWorker(name string, config *config.HorizonConfig, db *bolt.DB, am *AuthenticationManager) *ResourceWorker {

	var ec *worker.BaseExchangeContext
	dev, _ := persistence.FindExchangeDevice(db)
	if dev != nil {
		ec = worker.NewExchangeContext(fmt.Sprintf("%v/%v", dev.Org, dev.Id), dev.Token, config.Edge.ExchangeURL, config.Collaborators.HTTPClientFactory)
	}

	worker := &ResourceWorker{
		BaseWorker: worker.NewBaseWorker(name, config, ec),
		db:         db,
		rm:         NewResourceManager(config),
		am:         am,
	}

	glog.Info(reslog(fmt.Sprintf("Starting Resource worker")))
	// Establish the no work interval at 1 hour for garbage collection of resources.
	worker.Start(worker, 3600)
	return worker
}

func (w *ResourceWorker) Messages() chan events.Message {
	return w.BaseWorker.Manager.Messages
}

// Handle events that are propogated to this worker from the internal event bus.
func (w *ResourceWorker) NewEvent(incoming events.Message) {

	switch incoming.(type) {

	case *events.EdgeRegisteredExchangeMessage:
		msg, _ := incoming.(*events.EdgeRegisteredExchangeMessage)
		w.EC = worker.NewExchangeContext(fmt.Sprintf("%v/%v", msg.Org(), msg.DeviceId()), msg.Token(), w.Config.Edge.ExchangeURL, w.Config.Collaborators.HTTPClientFactory)
		w.Commands <- NewNodeConfigCommand(msg)

	case *events.NodeShutdownCompleteMessage:
		msg, _ := incoming.(*events.NodeShutdownCompleteMessage)
		switch msg.Event().Id {
		case events.UNCONFIGURE_COMPLETE:
			w.Commands <- NewNodeUnconfigCommand(msg)
		}

	default: //nothing

	}

	return
}

// Handle commands that are placed on the command queue.
func (w *ResourceWorker) CommandHandler(command worker.Command) bool {

	switch command.(type) {
	case *NodeConfigCommand:
		cmd, _ := command.(*NodeConfigCommand)
		if err := w.handleNodeConfigCommand(cmd); err != nil {
			glog.Errorf(reslog(fmt.Sprintf("Error handling node config command: %v", err)))
		}

	case *NodeUnconfigCommand:
		cmd, _ := command.(*NodeUnconfigCommand)
		if err := w.handleNodeUnconfigCommand(cmd); err != nil {
			glog.Errorf(reslog(fmt.Sprintf("Error handling node unconfig command: %v", err)))
		}

	default:
		return false
	}
	return true

}

// This function gets called when the worker framework has found nothing to do for the "no work interval"
// that was set when the worker was started.
func (w *ResourceWorker) NoWorkHandler() {
	glog.V(5).Infof(reslog(fmt.Sprintf("beginning garbage collection.")))

	glog.V(5).Infof(reslog(fmt.Sprintf("ending garbage collection.")))

}

// The node has just been configured so we can start functions that need node credentials to login.
func (w *ResourceWorker) handleNodeConfigCommand(cmd *NodeConfigCommand) error {
	if cmd.msg.Pattern() != "" {
		w.rm.NodeConfigUpdate(cmd.msg.Org(), cmd.msg.Pattern(), cmd.msg.DeviceId(), cmd.msg.Token())
		return w.rm.StartFileSyncService(w.am)
	}
	return nil
}

// The node has just been unconfigured so we can stop the file sync service.
func (w *ResourceWorker) handleNodeUnconfigCommand(cmd *NodeUnconfigCommand) error {
	w.rm.StopFileSyncService()
	w.Commands <- worker.NewTerminateCommand("shutdown")
	return nil
}

// Utility logging function
var reslog = func(v interface{}) string {
	return fmt.Sprintf("Resource Worker: %v", v)
}
