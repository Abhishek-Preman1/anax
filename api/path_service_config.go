package api

import (
	"errors"
	"fmt"
	"github.com/boltdb/bolt"
	"github.com/golang/glog"
	"github.com/open-horizon/anax/config"
	"github.com/open-horizon/anax/cutil"
	"github.com/open-horizon/anax/eventlog"
	"github.com/open-horizon/anax/events"
	"github.com/open-horizon/anax/exchange"
	"github.com/open-horizon/anax/microservice"
	"github.com/open-horizon/anax/persistence"
	"github.com/open-horizon/anax/policy"
	"strconv"
	"strings"
)

func LogServiceEvent(db *bolt.DB, severity string, message string, event_code string, service *Service) {
	surl := ""
	org := ""
	version := "[0.0.0,INFINITY)"
	arch := ""
	if service != nil {
		if service.Url != nil {
			surl = *service.Url
		}
		if service.Org != nil {
			org = *service.Org
		}
		if service.Arch != nil {
			arch = *service.Arch
		}
		if service.VersionRange != nil {
			version = *service.VersionRange
		}
	}

	eventlog.LogServiceEvent2(db, severity, message, event_code, "", surl, org, version, arch, []string{})
}

func findPoliciesForOutput(pm *policy.PolicyManager, db *bolt.DB) (map[string]policy.Policy, error) {

	out := make(map[string]policy.Policy)

	// Policies are kept in org specific directories
	allOrgs := pm.GetAllPolicyOrgs()
	for _, org := range allOrgs {

		allPolicies := pm.GetAllPolicies(org)
		for _, pol := range allPolicies {

			// the arch of SPecRefs have been converted to canonical arch in the pm, we will switch to the ones defined in the pattern or by user for output
			if pol.APISpecs != nil {
				for i := 0; i < len(pol.APISpecs); i++ {
					api_spec := &pol.APISpecs[i]
					if pmsdef, err := persistence.FindMicroserviceDefs(db, []persistence.MSFilter{persistence.UnarchivedMSFilter(), persistence.UrlOrgMSFilter(api_spec.SpecRef, org)}); err != nil {
						glog.Warningf(apiLogString(fmt.Sprintf("Failed to get service %v/%v from local db. %v", api_spec.Org, api_spec.SpecRef, err)))
					} else if pmsdef != nil && len(pmsdef) > 0 {
						api_spec.Arch = pmsdef[0].Arch
					}
				}
			}

			out[pol.Header.Name] = pol
		}
	}

	return out, nil
}

func FindServiceConfigForOutput(pm *policy.PolicyManager, db *bolt.DB) (map[string][]MicroserviceConfig, error) {

	outConfig := make([]MicroserviceConfig, 0, 10)

	// Get all the policies so that we can grab the pieces we need from there
	policies, err := findPoliciesForOutput(pm, db)
	if err != nil {
		return nil, errors.New(fmt.Sprintf("unable to get local policies, error %v", err))
	}

	// Each policy has some data we need for creating the output object. There is also data
	// in the microservice definition database and the attibutes in the attribute database.
	for _, pol := range policies {
		msURL := pol.APISpecs[0].SpecRef
		msOrg := pol.APISpecs[0].Org
		msVer := pol.APISpecs[0].Version
		mc := NewMicroserviceConfig(msURL, msOrg, msVer)

		// Find the microservice definition in our database so that we can get the upgrade settings.
		msDefs, err := persistence.FindMicroserviceDefs(db, []persistence.MSFilter{persistence.UrlOrgVersionMSFilter(msURL, msOrg, msVer), persistence.UnarchivedMSFilter()})
		if err != nil {
			return nil, errors.New(fmt.Sprintf("unable to get service definitions from the database, error %v", err))
		} else if msDefs != nil && len(msDefs) > 0 {
			mc.AutoUpgrade = msDefs[0].AutoUpgrade
			mc.ActiveUpgrade = msDefs[0].ActiveUpgrade
		} else {
			// take the default
			mc.AutoUpgrade = microservice.MS_DEFAULT_AUTOUPGRADE
			mc.ActiveUpgrade = microservice.MS_DEFAULT_ACTIVEUPGRADE
		}

		// Get the attributes for this service from the attributes database
		if attrs, err := persistence.FindApplicableAttributes(db, msURL, msOrg); err != nil {
			return nil, errors.New(fmt.Sprintf("unable to get service attributes from the database, error %v", err))
		} else {
			mc.Attributes = attrs
		}

		// Add the microservice config to the output array
		outConfig = append(outConfig, *mc)
	}

	out := make(map[string][]MicroserviceConfig)
	out["config"] = outConfig

	return out, nil
}

// Given a demarshalled Service object, validate it and save it, returning any errors.
func CreateService(service *Service,
	errorhandler ErrorHandler,
	getPatterns exchange.PatternHandler,
	resolveService exchange.ServiceResolverHandler,
	getService exchange.ServiceHandler,
	db *bolt.DB,
	config *config.HorizonConfig,
	from_user bool) (bool, *Service, *events.PolicyCreatedMessage) {

	org_forlog := ""
	if service.Org != nil {
		org_forlog = *service.Org
	}
	url_forlog := ""
	if service.Url != nil {
		url_forlog = *service.Url
	}
	if from_user {
		LogServiceEvent(db, persistence.SEVERITY_INFO, fmt.Sprintf("Start service configuration with user input for %v/%v.", org_forlog, url_forlog), persistence.EC_START_SERVICE_CONFIG, service)
	} else {
		LogServiceEvent(db, persistence.SEVERITY_INFO, fmt.Sprintf("Start service auto configuration for %v/%v.", org_forlog, url_forlog), persistence.EC_START_SERVICE_CONFIG, service)
	}

	// Check for the device in the local database. If there are errors, they will be written
	// to the HTTP response.
	pDevice, err := persistence.FindExchangeDevice(db)
	if err != nil {
		return errorhandler(NewSystemError(fmt.Sprintf("Unable to read horizondevice object, error %v", err))), nil, nil
	} else if pDevice == nil {
		return errorhandler(NewAPIUserInputError("Exchange registration not recorded. Complete account and device registration with an exchange and then record device registration using this API's /horizondevice path.", "service")), nil, nil
	}

	glog.V(5).Infof(apiLogString(fmt.Sprintf("Create service payload: %v", service)))

	// Validate all the inputs in the service object.
	if *service.Url == "" {
		return errorhandler(NewAPIUserInputError("not specified", "service.url")), nil, nil
	}
	if bail := checkInputString(errorhandler, "service.url", service.Url); bail {
		return true, nil, nil
	}

	// Use the device's org if org not specified in the service object.
	if service.Org == nil || *service.Org == "" {
		service.Org = &pDevice.Org
	} else if bail := checkInputString(errorhandler, "service.organization", service.Org); bail {
		return true, nil, nil
	}

	// We might be registering a dependent service, so look through the pattern and get a list of all dependent services, then
	// come up with a common version for all references. If the service we're registering is one of these, then use the
	// common version range in our service instead of the version range that was passed as input.
	if pDevice.Pattern != "" && from_user {

		pattern_org, pattern_name, _ := persistence.GetFormatedPatternString(pDevice.Pattern, pDevice.Org)

		common_apispec_list, _, err := getSpecRefsForPattern(pattern_name, pattern_org, getPatterns, resolveService, db, config, false)

		if err != nil {
			return errorhandler(err), nil, nil
		}

		if len(*common_apispec_list) != 0 {
			for _, apiSpec := range *common_apispec_list {
				if apiSpec.SpecRef == *service.Url && apiSpec.Org == *service.Org {
					service.VersionRange = &apiSpec.Version
					service.Arch = &apiSpec.Arch
					break
				}
			}
		}
	}

	// Return error if the arch in the service object is not a synonym of the node's arch.
	// Use the device's arch if not specified in the service object.
	thisArch := cutil.ArchString()
	if service.Arch == nil || *service.Arch == "" {
		service.Arch = &thisArch
	} else if *service.Arch != thisArch && config.ArchSynonyms.GetCanonicalArch(*service.Arch) != thisArch {
		return errorhandler(NewAPIUserInputError(fmt.Sprintf("arch %v is not supported by this node.", *service.Arch), "service.arch")), nil, nil
	} else if bail := checkInputString(errorhandler, "service.arch", service.Arch); bail {
		return true, nil, nil
	}

	// The versionRange field is checked for valid characters by the Version_Expression_Factory, it has a very
	// specific syntax and allows a subset of normally valid characters.

	// Use a default sensor version that allows all version if not specified.
	if service.VersionRange == nil || *service.VersionRange == "" {
		def := "0.0.0"
		service.VersionRange = &def
	}

	// Convert the sensor version to a version expression.
	vExp, err := policy.Version_Expression_Factory(*service.VersionRange)
	if err != nil {
		return errorhandler(NewAPIUserInputError(fmt.Sprintf("versionRange %v cannot be converted to a version expression, error %v", *service.VersionRange, err), "service.versionRange")), nil, nil
	}

	// Verify with the exchange to make sure the service definition is readable by this node.
	var msdef *persistence.MicroserviceDefinition
	var sdef *exchange.ServiceDefinition
	var err1 error
	sdef, _, err1 = getService(*service.Url, *service.Org, vExp.Get_expression(), *service.Arch)
	if err1 != nil || sdef == nil {
		if *service.Arch == thisArch {
			// failed with user defined arch
			return errorhandler(NewAPIUserInputError(fmt.Sprintf("Unable to find the service definition using  %v/%v %v %v in the exchange.", *service.Org, *service.Url, vExp.Get_expression(), *service.Arch), "service")), nil, nil
		} else {
			// try node's arch
			sdef, _, err1 = getService(*service.Url, *service.Org, vExp.Get_expression(), thisArch)
			if err1 != nil || sdef == nil {
				return errorhandler(NewAPIUserInputError(fmt.Sprintf("Unable to find the service definition using  %v/%v %v %v in the exchange.", *service.Org, *service.Url, vExp.Get_expression(), thisArch), "service")), nil, nil
			}
		}
	}

	// Convert the service definition to a persistent format so that it can be saved to the db.
	msdef, err = microservice.ConvertServiceToPersistent(sdef, *service.Org)
	if err != nil {
		return errorhandler(NewAPIUserInputError(fmt.Sprintf("Error converting the service metadata to persistent.MicroserviceDefinition for %v/%v version %v, error %v", *service.Org, sdef.URL, sdef.Version, err), "service")), nil, nil
	}

	// Save some of the items in the MicroserviceDefinition object for use in the upgrading process.
	if service.Name != nil {
		msdef.Name = *service.Name
	} else {
		names := strings.Split(*service.Url, "/")
		msdef.Name = names[len(names)-1]
		service.Name = &msdef.Name
	}
	msdef.RequestedArch = *service.Arch
	msdef.UpgradeVersionRange = vExp.Get_expression()
	if service.AutoUpgrade != nil {
		msdef.AutoUpgrade = *service.AutoUpgrade
	}
	if service.ActiveUpgrade != nil {
		msdef.ActiveUpgrade = *service.ActiveUpgrade
	}

	// The service definition returned by the exchange might be newer than what was specified in the input service object, so we save
	// the actual version of the service so that we know if we need to upgrade in the future.
	service.VersionRange = &msdef.Version

	// Check if the service has been registered or not (currently only support one service registration)
	if pms, err := persistence.FindMicroserviceDefs(db, []persistence.MSFilter{persistence.UnarchivedMSFilter(), persistence.UrlOrgMSFilter(*service.Url, *service.Org)}); err != nil {
		return errorhandler(NewSystemError(fmt.Sprintf("Error accessing db to find service definition: %v", err))), nil, nil
	} else if pms != nil && len(pms) > 0 {
		// this is for the auto service registration case.
		if !from_user {
			LogServiceEvent(db, persistence.SEVERITY_INFO, fmt.Sprintf("Complete service auto configuration for %v/%v.", *service.Org, *service.Url), persistence.EC_SERVICE_CONFIG_COMPLETE, service)
		}
		return errorhandler(NewDuplicateServiceError(fmt.Sprintf("Duplicate registration for %v/%v %v %v. Only one registration per service is supported.", *service.Org, *service.Url, vExp.Get_expression(), cutil.ArchString()), "service")), nil, nil
	}

	// If there are no attributes associated with this request but the service requires some configuration, return an error.
	if service.Attributes == nil || (service.Attributes != nil && len(*service.Attributes) == 0) {
		if varname := msdef.NeedsUserInput(); varname != "" {
			return errorhandler(NewMSMissingVariableConfigError(fmt.Sprintf(cutil.ANAX_SVC_MISSING_VARIABLE, varname, cutil.FormOrgSpecUrl(*service.Url, *service.Org)), "service.[attribute].mappings")), nil, nil
		}
	}

	// Validate any attributes specified in the attribute list and convert them to persistent objects.
	// This attribute verifier makes sure that there is a mapped attribute which specifies values for all the non-default
	// user inputs in the specific service selected earlier.
	msdefAttributeVerifier := func(attr persistence.Attribute) (bool, error) {

		// Verfiy that all userInput variables are correctly typed and that non-defaulted userInput variables are specified
		// in a mapped property attribute.
		if msdef != nil && attr.GetMeta().Type == "UserInputAttributes" {

			// Loop through each input variable and verify that it is defined in the service's user input section, and that the
			// type matches.
			for varName, varValue := range attr.GetGenericMappings() {
				glog.V(5).Infof(apiLogString(fmt.Sprintf("checking input variable: %v", varName)))
				if ui := msdef.GetUserInputName(varName); ui != nil {
					if err := cutil.VerifyWorkloadVarTypes(varValue, ui.Type); err != nil {
						return errorhandler(NewAPIUserInputError(fmt.Sprintf(cutil.ANAX_SVC_WRONG_TYPE+"%v", varName, cutil.FormOrgSpecUrl(*service.Url, *service.Org), err), "variables")), nil
					}
				}
			}

			// Verify that non-default variables are present.
			for _, ui := range msdef.UserInputs {
				if ui.DefaultValue != "" {
					continue
				} else if _, ok := attr.GetGenericMappings()[ui.Name]; !ok {
					return errorhandler(NewMSMissingVariableConfigError(fmt.Sprintf(cutil.ANAX_SVC_MISSING_VARIABLE, ui.Name, cutil.FormOrgSpecUrl(*service.Url, *service.Org)), "service.[attribute].mappings")), nil
				}
			}
		}

		return false, nil
	}

	// This attribute verifier makes sure that nodes using a pattern dont try to use policy. When patterns are in use, all policy
	// comes from the pattern.
	patternedDeviceAttributeVerifier := func(attr persistence.Attribute) (bool, error) {

		// If the device declared itself to be using a pattern, then it CANNOT specify any attributes that generate policy settings.
		if pDevice.Pattern != "" {
			if attr.GetMeta().Type == "MeteringAttributes" || attr.GetMeta().Type == "PropertyAttributes" || attr.GetMeta().Type == "CounterPartyPropertyAttributes" || attr.GetMeta().Type == "AgreementProtocolAttributes" {
				return errorhandler(NewAPIUserInputError(fmt.Sprintf("device is using a pattern %v, policy attributes are not supported.", pDevice.Pattern), "service.[attribute].type")), nil
			}
		}

		return false, nil
	}

	var attributes []persistence.Attribute
	if service.Attributes != nil {
		// build a serviceAttribute for each one
		var err error
		var inputErrWritten bool

		attributes, inputErrWritten, err = toPersistedAttributesAttachedToService(errorhandler, pDevice, config.Edge.DefaultServiceRegistrationRAM, *service.Attributes, persistence.NewServiceSpec(*service.Url, *service.Org), []AttributeVerifier{msdefAttributeVerifier, patternedDeviceAttributeVerifier})
		if !inputErrWritten && err != nil {
			return errorhandler(NewSystemError(fmt.Sprintf("Failure deserializing attributes: %v", err))), nil, nil
		} else if inputErrWritten {
			return true, nil, nil
		}
	}

	// Information advertised in the edge node policy file
	var haPartner []string
	var meterPolicy policy.Meter
	var counterPartyProperties policy.RequiredProperty
	var properties map[string]interface{}
	var globalAgreementProtocols []interface{}

	props := make(map[string]interface{})

	hasAA := false
	// There might be node wide global attributes. Check for them and grab the values to use as defaults for later.
	allAttrs, aerr := persistence.FindApplicableAttributes(db, "", "")
	if aerr != nil {
		return errorhandler(NewSystemError(fmt.Sprintf("Unable to fetch global attributes, error %v", err))), nil, nil
	}

	// For each node wide attribute, extract the value and save it for use later in this function.
	for _, attr := range allAttrs {

		// get service specs
		sps := persistence.GetAttributeServiceSpecs(&attr)

		apply_to_all := false
		if sps == nil || len(*sps) == 0 {
			apply_to_all = true
		}

		// Extract HA property
		if attr.GetMeta().Type == "HAAttributes" {
			haPartner = attr.(persistence.HAAttributes).Partners
			glog.V(5).Infof(apiLogString(fmt.Sprintf("Found default global HA attribute %v", attr)))
		}

		if attr.GetMeta().Type == "ArchitectureAttributes" {
			hasAA = true
		}

		// Global policy attributes are ignored for devices that are using a pattern. All policy is controlled
		// by the pattern definition.
		if pDevice.Pattern == "" {

			// Extract global metering property
			if attr.GetMeta().Type == "MeteringAttributes" && apply_to_all {
				// found a global metering entry
				meterPolicy = policy.Meter{
					Tokens:                attr.(persistence.MeteringAttributes).Tokens,
					PerTimeUnit:           attr.(persistence.MeteringAttributes).PerTimeUnit,
					NotificationIntervalS: attr.(persistence.MeteringAttributes).NotificationIntervalS,
				}
				glog.V(5).Infof(apiLogString(fmt.Sprintf("Found default global metering attribute %v", attr)))
			}

			// Extract global counterparty property
			if attr.GetMeta().Type == "CounterPartyPropertyAttributes" {
				counterPartyProperties = attr.(persistence.CounterPartyPropertyAttributes).Expression
				glog.V(5).Infof(apiLogString(fmt.Sprintf("Found default global counterpartyproperty attribute %v", attr)))
			}

			// Extract global properties
			if attr.GetMeta().Type == "PropertyAttributes" && apply_to_all {
				properties = attr.(persistence.PropertyAttributes).Mappings
				glog.V(5).Infof(apiLogString(fmt.Sprintf("Found default global properties %v", properties)))
			}

			// Extract global agreement protocol attribute
			if attr.GetMeta().Type == "AgreementProtocolAttributes" && apply_to_all {
				agpl := attr.(persistence.AgreementProtocolAttributes).Protocols
				globalAgreementProtocols = agpl.([]interface{})
				glog.V(5).Infof(apiLogString(fmt.Sprintf("Found default global agreement protocol attribute %v", globalAgreementProtocols)))
			}
		}
	}

	// If an HA device has no HA attribute then the configuration is invalid.
	if pDevice.HA && len(haPartner) == 0 {
		return errorhandler(NewAPIUserInputError("services on an HA device must specify an HA partner.", "service.[attribute].type")), nil, nil
	}

	// Persist all attributes on this service, and while we're at it, fetch the attribute values we need for the node side policy file.
	// Any policy attributes we find will overwrite values set in a global attribute of the same type.
	var serviceAgreementProtocols []policy.AgreementProtocol
	for _, attr := range attributes {

		// there may be multiple ArchitectureAttributes, we only take the first one.
		// here we assume all the services have the same arch which is defined by the cutil.ArchString()
		if hasAA && attr.GetMeta().Type == "ArchitectureAttributes" {
			continue
		}

		_, err := persistence.SaveOrUpdateAttribute(db, attr, "", false)
		if err != nil {
			return errorhandler(NewSystemError(fmt.Sprintf("error saving attribute %v, error %v", attr, err))), nil, nil
		}

		switch attr.(type) {
		case *persistence.ComputeAttributes:
			compute := attr.(*persistence.ComputeAttributes)
			props["cpus"] = strconv.FormatInt(compute.CPUs, 10)
			props["ram"] = strconv.FormatInt(compute.RAM, 10)

		case *persistence.HAAttributes:
			haPartner = attr.(*persistence.HAAttributes).Partners

		case *persistence.MeteringAttributes:
			meterPolicy = policy.Meter{
				Tokens:                attr.(*persistence.MeteringAttributes).Tokens,
				PerTimeUnit:           attr.(*persistence.MeteringAttributes).PerTimeUnit,
				NotificationIntervalS: attr.(*persistence.MeteringAttributes).NotificationIntervalS,
			}

		case *persistence.CounterPartyPropertyAttributes:
			counterPartyProperties = attr.(*persistence.CounterPartyPropertyAttributes).Expression

		case *persistence.PropertyAttributes:
			properties = attr.(*persistence.PropertyAttributes).Mappings

		case *persistence.AgreementProtocolAttributes:
			agpl := attr.(*persistence.AgreementProtocolAttributes).Protocols
			serviceAgreementProtocols = agpl.([]policy.AgreementProtocol)

		default:
			glog.V(4).Infof(apiLogString(fmt.Sprintf("Unhandled attr type (%T): %v", attr, attr)))
		}
	}

	// Add the PropertyAttributes to props. There are several attribute types that contribute properties to the properties
	// section of the policy file.
	if len(properties) > 0 {
		for key, val := range properties {
			glog.V(5).Infof(apiLogString(fmt.Sprintf("Adding property %v=%v with value type %T", key, val, val)))
			props[key] = val
		}
	}

	glog.V(5).Infof(apiLogString(fmt.Sprintf("Complete Attr list for registration of service %v/%v: %v", *service.Org, *service.Url, attributes)))

	// Establish the correct agreement protocol list. The AGP list from this service overrides any global list that might exist.
	var agpList *[]policy.AgreementProtocol
	if len(serviceAgreementProtocols) != 0 {
		agpList = &serviceAgreementProtocols
	} else if list, err := policy.ConvertToAgreementProtocolList(globalAgreementProtocols); err != nil {
		return errorhandler(NewSystemError(fmt.Sprintf("Error converting global agreement protocol list attribute %v to agreement protocol list, error: %v", globalAgreementProtocols, err))), nil, nil
	} else {
		agpList = list
	}

	// Save the service definition in the local database.
	if err := persistence.SaveOrUpdateMicroserviceDef(db, msdef); err != nil {
		return errorhandler(NewSystemError(fmt.Sprintf("Error saving service definition %v into db: %v", *msdef, err))), nil, nil
	}

	// Set max number of agreements for this service's policy.
	maxAgreements := 1
	if msdef.Sharable == exchange.MS_SHARING_MODE_SINGLETON || msdef.Sharable == exchange.MS_SHARING_MODE_MULTIPLE || msdef.Sharable == exchange.MS_SHARING_MODE_SINGLE {
		if pDevice.Pattern == "" {
			maxAgreements = 5 // hard coded to 5 for now. will change to 0 later
		} else {
			maxAgreements = 0 // no limites for pattern
		}
	}

	glog.V(5).Infof(apiLogString(fmt.Sprintf("Create service: %v", service)))

	// Generate a policy based on all the attributes and the service definition.
	if msg, genErr := policy.GeneratePolicy(*service.Url, *service.Org, *service.Name, *service.VersionRange, *service.Arch, &props, haPartner, meterPolicy, counterPartyProperties, *agpList, maxAgreements, config.Edge.PolicyPath, pDevice.Org); genErr != nil {
		return errorhandler(NewSystemError(fmt.Sprintf("Error generating policy, error: %v", genErr))), nil, nil
	} else {
		if from_user {
			LogServiceEvent(db, persistence.SEVERITY_INFO, fmt.Sprintf("Complete service configuration for %v/%v.", *service.Org, *service.Url), persistence.EC_SERVICE_CONFIG_COMPLETE, service)
		} else {
			LogServiceEvent(db, persistence.SEVERITY_INFO, fmt.Sprintf("Complete service auto configuration for %v/%v.", *service.Org, *service.Url), persistence.EC_SERVICE_CONFIG_COMPLETE, service)
		}
		return false, service, msg
	}
}
