package service

import (
	"encoding/json"
	"fmt"
	"github.com/open-horizon/anax/api"
	"github.com/open-horizon/anax/cli/cliutils"
	"github.com/open-horizon/anax/cutil"
	"github.com/open-horizon/anax/exchange"
	"github.com/open-horizon/anax/persistence"
	"github.com/open-horizon/anax/policy"
	"net/http"
)

type APIServices struct {
	Config []api.APIMicroserviceConfig `json:"config"` // the service configurations
	//Instances   map[string][]interface{}    `json:"instances"`   // the service instances that are running
	//Definitions map[string][]interface{}    `json:"definitions"` // the definitions of services from the exchange
}

type OurService struct {
	Url       string                 `json:"url"`     // A URL pointing to the definition of the service
	Org       string                 `json:"org"`     // The organization where the service is defined
	Version   string                 `json:"version"` // The version of the service in OSGI version format
	Arch      string                 `json:"arch"`    // The hardware architecture of the service impl
	Variables map[string]interface{} `json:"variables"`
}

func List() {
	// Get the services
	var apiOutput APIServices
	// Note: intentionally querying /service, because in the future we will probably want to mix in some key runtime info
	httpCode := cliutils.HorizonGet("service/config", []int{200, cliutils.ANAX_NOT_CONFIGURED_YET}, &apiOutput)
	//todo: i think config can be queried even before the node is registered?
	if httpCode == cliutils.ANAX_NOT_CONFIGURED_YET {
		cliutils.Fatal(cliutils.HTTP_ERROR, cliutils.MUST_REGISTER_FIRST)
	}

	// Go thru the services and pull out interesting fields
	services := make([]OurService, 0)
	for _, s := range apiOutput.Config {
		arch := cutil.ArchString()
		serv := OurService{Url: s.SensorUrl, Org: s.SensorOrg, Version: s.SensorVersion, Arch: arch, Variables: make(map[string]interface{})}

		for _, attr := range s.Attributes {
			if b_attr, err := json.Marshal(attr); err != nil {
				cliutils.Fatal(cliutils.JSON_PARSING_ERROR, "failed to marshal '/service/config' output attribute %v. %v", attr, err)
				return
			} else if a, err := persistence.HydrateConcreteAttribute(b_attr); err != nil {
				cliutils.Fatal(cliutils.JSON_PARSING_ERROR, "failed to convert '/service/config' output attribute %v to its original type. %v", attr, err)
				return
			} else {
				switch a.(type) {
				case persistence.ArchitectureAttributes:
					// get arch
					serv.Arch = a.(persistence.ArchitectureAttributes).Architecture
				case persistence.UserInputAttributes:
					// get user input
					serv.Variables = a.GetGenericMappings()
				}
			}
		}

		services = append(services, serv)
	}

	// Convert to json and output
	jsonBytes, err := json.MarshalIndent(services, "", cliutils.JSON_INDENT)
	if err != nil {
		cliutils.Fatal(cliutils.JSON_PARSING_ERROR, "failed to marshal 'hzn service list' output: %v", err)
	}
	fmt.Printf("%s\n", jsonBytes)
}

func Registered() {
	// The registered services are listed as policies
	apiOutput := make(map[string]policy.Policy)
	httpCode := cliutils.HorizonGet("service/policy", []int{200, cliutils.ANAX_NOT_CONFIGURED_YET}, &apiOutput)
	if httpCode == cliutils.ANAX_NOT_CONFIGURED_YET {
		cliutils.Fatal(cliutils.HTTP_ERROR, cliutils.MUST_REGISTER_FIRST)
	}

	// Convert to json and output
	jsonBytes, err := json.MarshalIndent(apiOutput, "", cliutils.JSON_INDENT)
	if err != nil {
		cliutils.Fatal(cliutils.JSON_PARSING_ERROR, "failed to marshal 'hzn service registered' output: %v", err)
	}
	fmt.Printf("%s\n", jsonBytes)
}

func ListConfigState() {
	apiOutput := make(map[string][]exchange.ServiceConfigState)
	httpCode := cliutils.HorizonGet("service/configstate", []int{200, cliutils.ANAX_NOT_CONFIGURED_YET}, &apiOutput)
	if httpCode == cliutils.ANAX_NOT_CONFIGURED_YET {
		cliutils.Fatal(cliutils.HTTP_ERROR, cliutils.MUST_REGISTER_FIRST)
	}

	// Convert to json and output
	jsonBytes, err := json.MarshalIndent(apiOutput, "", cliutils.JSON_INDENT)
	if err != nil {
		cliutils.Fatal(cliutils.JSON_PARSING_ERROR, "failed to marshal 'hzn service configstate' output: %v", err)
	}
	fmt.Printf("%s\n", jsonBytes)
}

func Suspend(forceSuspend bool, applyAll bool, serviceOrg string, serviceUrl string) {
	msg_part := "all the registered services"
	if !applyAll {
		if serviceOrg != "" {
			if serviceUrl != "" {
				msg_part = fmt.Sprintf("service %v/%v", serviceOrg, serviceUrl)
			} else {
				msg_part = fmt.Sprintf("all the registered services from organization %v", serviceOrg)
			}
		} else if serviceUrl != "" {
			cliutils.Fatal(cliutils.CLI_INPUT_ERROR, "Please specify the organization for service %v.", serviceUrl)
		}
	}

	if !forceSuspend {
		cliutils.ConfirmRemove(fmt.Sprintf("Are you sure you want to suspend %v for this Horizon node?", msg_part))
	}

	fmt.Printf("Suspending %v, cancelling releated agreements, stopping related service containers...", msg_part)
	fmt.Println("")

	if applyAll {
		serviceOrg = ""
		serviceUrl = ""
	}
	apiInput := exchange.ServiceConfigState{
		Url:         serviceUrl,
		Org:         serviceOrg,
		ConfigState: exchange.SERVICE_CONFIGSTATE_SUSPENDED,
	}

	cliutils.HorizonPutPost(http.MethodPost, "service/configstate", []int{201, 200}, apiInput)

	fmt.Println("Service suspending request sucessfully sent, please use 'hzn agreement' and 'docker ps' to make sure the related agreements and service containers are removed. It may take a couple of minutes.")
}

func Resume(applyAll bool, serviceOrg string, serviceUrl string) {
	msg_part := "all the registered services"
	if !applyAll {
		if serviceOrg != "" {
			if serviceUrl != "" {
				msg_part = fmt.Sprintf("service %v/%v", serviceOrg, serviceUrl)
			} else {
				msg_part = fmt.Sprintf("all the registered services from organization %v", serviceOrg)
			}
		} else if serviceUrl != "" {
			cliutils.Fatal(cliutils.CLI_INPUT_ERROR, "Please specify the organization for service %v.", serviceUrl)
		}
	}

	fmt.Printf("Resuming %v ...", msg_part)
	fmt.Println("")

	if applyAll {
		serviceOrg = ""
		serviceUrl = ""
	}
	apiInput := exchange.ServiceConfigState{
		Url:         serviceUrl,
		Org:         serviceOrg,
		ConfigState: exchange.SERVICE_CONFIGSTATE_ACTIVE,
	}

	cliutils.HorizonPutPost(http.MethodPost, "service/configstate", []int{201, 200}, apiInput)

	fmt.Println("Service resuming request sucessfully sent, please use 'hzn agreement' and 'docker ps' to make sure the related agreements and service containers are started. It may take a couple of minutes.")
}
