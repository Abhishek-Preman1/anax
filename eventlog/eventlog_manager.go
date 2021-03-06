package eventlog

import (
	"github.com/boltdb/bolt"
	"github.com/open-horizon/anax/persistence"
)

// Save the eventlog into the db
func LogEvent(db *bolt.DB, severity string, message string, event_code string, source_type string, source persistence.EventSourceInterface) error {
	eventlog := persistence.NewEventLog(severity, message, event_code, source_type, source)
	return persistence.SaveEventLog(db, eventlog)
}

// Save the agreement eventlog into the db
func LogAgreementEvent(db *bolt.DB, severity string, message string, event_code string, ag persistence.EstablishedAgreement) error {
	source := persistence.NewAgreementEventSourceFromAg(ag)
	eventlog := persistence.NewEventLog(severity, message, event_code, persistence.SRC_TYPE_AG, source)
	return persistence.SaveEventLog(db, eventlog)
}

// Save the agreement eventlog into the db
func LogAgreementEvent2(db *bolt.DB, severity, message, event_code, agreement_id string, workload persistence.WorkloadInfo, dependent_svcs persistence.ServiceSpecs, consumer_id, protocol string) error {
	source := persistence.NewAgreementEventSource(agreement_id, workload, dependent_svcs, consumer_id, protocol)
	eventlog := persistence.NewEventLog(severity, message, event_code, persistence.SRC_TYPE_AG, source)
	return persistence.SaveEventLog(db, eventlog)
}

// Save the service eventlog into the db
func LogServiceEvent(db *bolt.DB, severity string, message string, event_code string, msi persistence.MicroserviceInstance) error {
	source := persistence.NewServiceEventSourceFromServiceInstance(msi)
	eventlog := persistence.NewEventLog(severity, message, event_code, persistence.SRC_TYPE_SVC, source)
	return persistence.SaveEventLog(db, eventlog)
}

// Save the service eventlog into the db
func LogServiceEvent2(db *bolt.DB, severity, message, event_code, instance_id, service_url, org, version, arch string, agreement_ids []string) error {
	source := persistence.NewServiceEventSource(instance_id, service_url, org, version, arch, agreement_ids)
	eventlog := persistence.NewEventLog(severity, message, event_code, persistence.SRC_TYPE_SVC, source)
	return persistence.SaveEventLog(db, eventlog)
}

// Save the service eventlog into the db
func LogServiceEvent3(db *bolt.DB, severity string, message string, event_code string, msdef persistence.MicroserviceDefinition) error {
	source := persistence.NewServiceEventSourceFromServiceDef(msdef)
	eventlog := persistence.NewEventLog(severity, message, event_code, persistence.SRC_TYPE_SVC, source)
	return persistence.SaveEventLog(db, eventlog)
}

// Save the node eventlog into the db
func LogNodeEvent(db *bolt.DB, severity, message, event_code, node_id, org, pattern, config_state string) error {
	source := persistence.NewNodeEventSource(node_id, org, pattern, config_state)
	eventlog := persistence.NewEventLog(severity, message, event_code, persistence.SRC_TYPE_NODE, source)
	return persistence.SaveEventLog(db, eventlog)
}

// Save the database eventlog into the db
func LogDatabaseEvent(db *bolt.DB, severity, message, event_code string) error {
	source := persistence.NewDatabaseEventSource()
	eventlog := persistence.NewEventLog(severity, message, event_code, persistence.SRC_TYPE_DB, source)
	return persistence.SaveEventLog(db, eventlog)
}

// Save the database eventlog into the db
func LogExchangeEvent(db *bolt.DB, severity, message, event_code, exchange_url string) error {
	source := persistence.NewExchangeEventSource(exchange_url)
	eventlog := persistence.NewEventLog(severity, message, event_code, persistence.SRC_TYPE_EXCH, source)
	return persistence.SaveEventLog(db, eventlog)
}

// Get event logs from the db.
// If all_logs is false, only the event logs for the current registration is returned.
// The input selectors is a map of selector array.
// For example:
//   selectors = [string][]Selector{
//			"a": [{"~": "test"}, {"~", "agreement"}],
//          "b": [{"=", "this is a test"}],
//			"c":[{">", 100}]
//		}
// It means checking if this event log matches the following logic:
//    the attribute "a" contains the word "test" and "agreement",
//    attribute "b" equals "this is a test" and attribute "c" is greater than 100.
// A real example is:
//   { 	"severity": [{"=", "info"}],
//		"message": [{"~", "agreement"}, {"~", "service"}],
//		"agreement_id": [{"=", c47db9ec232ae4b32c98c08579efcc420aa7652e5fe23d04289c8315c17a04ab}]
//   }
func GetEventLogs(db *bolt.DB, all_logs bool, selectors map[string][]persistence.Selector) ([]persistence.EventLog, error) {
	return persistence.FindEventLogsWithSelectors(db, all_logs, selectors)
}
